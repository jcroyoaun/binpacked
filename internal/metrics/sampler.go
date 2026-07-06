// Package metrics samples the Kubernetes metrics API (metrics-server) on an
// interval and keeps a bounded in-memory window of per-container usage, so
// right-sizing recommendations can be based on observed behavior rather than
// spec values alone.
package metrics

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/zillow/binpacked/internal/packing"
)

type sample struct {
	ts       time.Time
	cpuMilli int64
	memBytes int64
}

type containerKey struct {
	namespace string
	pod       string
	container string
}

type ring struct {
	samples  []sample
	lastSeen time.Time
}

// Sampler polls PodMetrics and retains up to MaxSamples distinct measurements
// per container. It implements packing.UsageProvider.
type Sampler struct {
	Client     metricsclient.Interface
	Interval   time.Duration
	MaxSamples int // max samples kept per container
	Logger     *log.Logger

	mu          sync.RWMutex
	byCont      map[containerKey]*ring
	available   bool
	lastSuccess time.Time
	lastErr     string
}

// New returns a Sampler with sane defaults applied.
func New(client metricsclient.Interface, interval time.Duration, maxSamples int, logger *log.Logger) *Sampler {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if maxSamples <= 0 {
		maxSamples = 120
	}
	if logger == nil {
		logger = log.Default()
	}
	return &Sampler{
		Client:     client,
		Interval:   interval,
		MaxSamples: maxSamples,
		Logger:     logger,
		byCont:     make(map[containerKey]*ring),
	}
}

// Run polls until ctx is canceled. The first poll happens immediately.
func (s *Sampler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()

	s.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.poll(ctx)
		}
	}
}

func (s *Sampler) poll(ctx context.Context) {
	pollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	list, err := s.Client.MetricsV1beta1().PodMetricses(metav1.NamespaceAll).List(pollCtx, metav1.ListOptions{})
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil {
		// Log transitions only, so an absent metrics-server doesn't spam.
		if s.lastErr != err.Error() {
			s.Logger.Printf("metrics sampler: metrics API unavailable (usage-based recommendations disabled until it recovers): %v", err)
			s.lastErr = err.Error()
		}
		s.available = false
		return
	}
	if s.lastErr != "" || !s.available {
		s.Logger.Printf("metrics sampler: metrics API available, %d pod metrics", len(list.Items))
		s.lastErr = ""
	}
	s.available = true
	s.lastSuccess = now

	for i := range list.Items {
		pm := &list.Items[i]
		for j := range pm.Containers {
			c := &pm.Containers[j]
			key := containerKey{namespace: pm.Namespace, pod: pm.Name, container: c.Name}
			r := s.byCont[key]
			if r == nil {
				r = &ring{}
				s.byCont[key] = r
			}
			r.lastSeen = now

			// metrics-server serves a cached point that only refreshes every
			// --metric-resolution; polling faster than that returns the same
			// measurement. Dedupe on the measurement timestamp so sample
			// counts reflect distinct observations.
			ts := pm.Timestamp.Time
			if n := len(r.samples); n > 0 && !ts.After(r.samples[n-1].ts) {
				continue
			}

			cpu := c.Usage[corev1.ResourceCPU]
			mem := c.Usage[corev1.ResourceMemory]
			r.samples = append(r.samples, sample{ts: ts, cpuMilli: cpu.MilliValue(), memBytes: mem.Value()})
			if len(r.samples) > s.MaxSamples {
				r.samples = r.samples[len(r.samples)-s.MaxSamples:]
			}
		}
	}

	// Drop containers not reported for two full windows (pod gone).
	cutoff := now.Add(-2 * time.Duration(s.MaxSamples) * s.Interval)
	for key, r := range s.byCont {
		if r.lastSeen.Before(cutoff) {
			delete(s.byCont, key)
		}
	}
}

// staleAfter is how long after the last successful poll the sampler keeps
// vouching for its data.
func (s *Sampler) staleAfter() time.Duration {
	d := 3 * s.Interval
	if d < 2*time.Minute {
		d = 2 * time.Minute
	}
	return d
}

// Available reports whether the metrics API responded recently.
func (s *Sampler) Available() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.available && time.Since(s.lastSuccess) < s.staleAfter()
}

// Window returns the maximum window the sampler can cover, in seconds.
// Per-container stats report their actually observed span.
func (s *Sampler) Window() int64 {
	return int64(time.Duration(s.MaxSamples) * s.Interval / time.Second)
}

// ContainerUsage implements packing.UsageProvider. It refuses to serve data
// once the metrics API has been unreachable long enough that the retained
// samples no longer describe the present.
func (s *Sampler) ContainerUsage(namespace, pod, container string) (packing.UsageStats, packing.UsageStats, bool) {
	s.mu.RLock()
	fresh := s.available && time.Since(s.lastSuccess) < s.staleAfter()
	r := s.byCont[containerKey{namespace: namespace, pod: pod, container: container}]
	var samples []sample
	if fresh && r != nil {
		samples = make([]sample, len(r.samples))
		copy(samples, r.samples)
	}
	s.mu.RUnlock()

	if len(samples) == 0 {
		return packing.UsageStats{}, packing.UsageStats{}, false
	}

	// Actual observed span of this container's measurements. A single
	// sample covers roughly one metrics-server resolution; approximate the
	// tail with the poll interval.
	span := samples[len(samples)-1].ts.Sub(samples[0].ts) + s.Interval
	windowSeconds := int64(span / time.Second)

	cpuVals := make([]int64, len(samples))
	memVals := make([]int64, len(samples))
	for i, sm := range samples {
		cpuVals[i] = sm.cpuMilli
		memVals[i] = sm.memBytes
	}
	return stats(cpuVals, windowSeconds), stats(memVals, windowSeconds), true
}

func stats(vals []int64, windowSeconds int64) packing.UsageStats {
	sorted := make([]int64, len(vals))
	copy(sorted, vals)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum int64
	for _, v := range sorted {
		sum += v
	}
	idx := (len(sorted)*95 + 99) / 100 // ceil(0.95 * n)
	if idx > 0 {
		idx--
	}
	return packing.UsageStats{
		AvgPerPod:     sum / int64(len(sorted)),
		MaxPerPod:     sorted[len(sorted)-1],
		P95PerPod:     sorted[idx],
		Samples:       len(sorted),
		WindowSeconds: windowSeconds,
	}
}
