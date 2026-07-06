package packing

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// UsageProvider supplies observed usage statistics for a container of a pod.
// Implementations must be safe for concurrent use. A nil UsageProvider (or
// ok=false) means no usage data is available.
type UsageProvider interface {
	// ContainerUsage returns cpu (millicores) and memory (bytes) stats for
	// one container of one pod.
	ContainerUsage(namespace, pod, container string) (cpu UsageStats, mem UsageStats, ok bool)
	// Window returns the sampling window currently covered, in seconds.
	Window() int64
}

// ResolveWorkload maps a pod to the workload that owns it using only data
// present on the pod. Deployment resolution relies on the pod-template-hash
// label kube-controller-manager stamps on ReplicaSet-managed pods; CronJob
// resolution is a name heuristic (exact resolution would need a Job informer).
func ResolveWorkload(pod *corev1.Pod) (kind, name string) {
	var owner *metaOwner
	for i := range pod.OwnerReferences {
		r := &pod.OwnerReferences[i]
		if r.Controller != nil && *r.Controller {
			owner = &metaOwner{kind: r.Kind, name: r.Name}
			break
		}
	}
	if owner == nil && len(pod.OwnerReferences) > 0 {
		owner = &metaOwner{kind: pod.OwnerReferences[0].Kind, name: pod.OwnerReferences[0].Name}
	}
	if owner == nil {
		return "Pod", pod.Name
	}

	switch owner.kind {
	case "ReplicaSet":
		if hash := pod.Labels["pod-template-hash"]; hash != "" && strings.HasSuffix(owner.name, "-"+hash) {
			return "Deployment", strings.TrimSuffix(owner.name, "-"+hash)
		}
		// Argo Rollouts manages ReplicaSets directly and stamps its own hash label.
		if hash := pod.Labels["rollouts-pod-template-hash"]; hash != "" && strings.HasSuffix(owner.name, "-"+hash) {
			return "Rollout", strings.TrimSuffix(owner.name, "-"+hash)
		}
		return "ReplicaSet", owner.name
	case "Job":
		// CronJob-created Jobs are named <cronjob>-<scheduled-unix-minutes>.
		// Only collapse when the numeric suffix is plausibly unix minutes
		// (2020-2100 = 26.3M-68.4M); this excludes YYYYMMDD dates (~20-21M)
		// and unix-seconds timestamps (10 digits).
		if i := strings.LastIndex(owner.name, "-"); i > 0 {
			suffix := owner.name[i+1:]
			if n, err := strconv.ParseInt(suffix, 10, 64); err == nil && n >= 26_000_000 && n <= 69_000_000 {
				return "CronJob", owner.name[:i]
			}
		}
		return "Job", owner.name
	case "Node":
		return "StaticPod", pod.Name
	default:
		return owner.kind, owner.name
	}
}

type metaOwner struct {
	kind string
	name string
}

// AggregateWorkloads groups pods by owning workload. Pods in Succeeded or
// Failed phase are excluded; pending (unscheduled) pods are included because
// they are part of a workload's requested footprint. Usage may be nil.
func AggregateWorkloads(pods []*corev1.Pod, usage UsageProvider) []WorkloadPacking {
	type contAgg struct {
		reqCPU, limCPU, reqMem, limMem int64 // per-pod maxima across replicas
		cpuStats, memStats             []UsageStats
	}
	type agg struct {
		ref         WorkloadRef
		podCount    int64
		pendingPods int64
		qos         map[string]int64
		isDS        bool
		nodes       map[string]struct{}
		containers  map[string]*contAgg
		contOrder   []string
	}

	byRef := make(map[WorkloadRef]*agg)
	var order []WorkloadRef

	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		kind, name := ResolveWorkload(pod)
		ref := WorkloadRef{Namespace: pod.Namespace, Kind: kind, Name: name}
		a := byRef[ref]
		if a == nil {
			a = &agg{
				ref:        ref,
				qos:        make(map[string]int64),
				nodes:      make(map[string]struct{}),
				containers: make(map[string]*contAgg),
				isDS:       kind == "DaemonSet",
			}
			byRef[ref] = a
			order = append(order, ref)
		}

		a.podCount++
		a.qos[qosClass(pod)]++
		if pod.Spec.NodeName != "" {
			a.nodes[pod.Spec.NodeName] = struct{}{}
		} else {
			a.pendingPods++
		}

		for _, c := range packingContainers(pod) {
			ca := a.containers[c.Name]
			if ca == nil {
				ca = &contAgg{}
				a.containers[c.Name] = ca
				a.contOrder = append(a.contOrder, c.Name)
			}
			ca.reqCPU = maxInt64(ca.reqCPU, c.Resources.Requests.Cpu().MilliValue())
			ca.limCPU = maxInt64(ca.limCPU, c.Resources.Limits.Cpu().MilliValue())
			ca.reqMem = maxInt64(ca.reqMem, c.Resources.Requests.Memory().Value())
			ca.limMem = maxInt64(ca.limMem, c.Resources.Limits.Memory().Value())

			if usage != nil {
				if cpuS, memS, ok := usage.ContainerUsage(pod.Namespace, pod.Name, c.Name); ok {
					ca.cpuStats = append(ca.cpuStats, cpuS)
					ca.memStats = append(ca.memStats, memS)
				}
			}
		}
	}

	result := make([]WorkloadPacking, 0, len(order))
	for _, ref := range order {
		a := byRef[ref]
		w := WorkloadPacking{
			WorkloadRef: a.ref,
			PodCount:    a.podCount,
			PendingPods: a.pendingPods,
			QOSClass:    dominantQOS(a.qos),
			IsDaemonSet: a.isDS,
		}
		for n := range a.nodes {
			w.Nodes = append(w.Nodes, n)
		}
		sort.Strings(w.Nodes)

		for _, cname := range a.contOrder {
			ca := a.containers[cname]
			cp := ContainerPacking{Name: cname}
			cp.CPU = workloadResource(ca.reqCPU, ca.limCPU, a.podCount, mergeStats(ca.cpuStats))
			cp.Memory = workloadResource(ca.reqMem, ca.limMem, a.podCount, mergeStats(ca.memStats))
			w.Containers = append(w.Containers, cp)

			w.CPU.RequestPerPod += cp.CPU.RequestPerPod
			w.CPU.LimitPerPod += cp.CPU.LimitPerPod
			w.Memory.RequestPerPod += cp.Memory.RequestPerPod
			w.Memory.LimitPerPod += cp.Memory.LimitPerPod
		}
		w.CPU.RequestTotal = w.CPU.RequestPerPod * a.podCount
		w.CPU.LimitTotal = w.CPU.LimitPerPod * a.podCount
		w.Memory.RequestTotal = w.Memory.RequestPerPod * a.podCount
		w.Memory.LimitTotal = w.Memory.LimitPerPod * a.podCount
		w.CPU.Usage = sumUsage(w.Containers, func(c ContainerPacking) *UsageStats { return c.CPU.Usage })
		w.Memory.Usage = sumUsage(w.Containers, func(c ContainerPacking) *UsageStats { return c.Memory.Usage })

		result = append(result, w)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Namespace != result[j].Namespace {
			return result[i].Namespace < result[j].Namespace
		}
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// ComputeWorkloads lists pods from the informer cache and aggregates them by
// owning workload.
func (c Computer) ComputeWorkloads() ([]WorkloadPacking, error) {
	pods, err := c.PodLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}
	return AggregateWorkloads(pods, c.Usage), nil
}

func workloadResource(reqPerPod, limPerPod, podCount int64, usage *UsageStats) WorkloadResource {
	return WorkloadResource{
		RequestPerPod: reqPerPod,
		LimitPerPod:   limPerPod,
		RequestTotal:  reqPerPod * podCount,
		LimitTotal:    limPerPod * podCount,
		Usage:         usage,
	}
}

// mergeStats combines per-pod usage stats into one per-pod view of the
// workload container: avg is sample-weighted, max/p95 take the worst pod,
// and the window is the longest span actually observed for any single pod —
// not the sampler's configured capacity — so evidence is never overstated.
func mergeStats(stats []UsageStats) *UsageStats {
	if len(stats) == 0 {
		return nil
	}
	var out UsageStats
	var weightedSum, totalSamples int64
	for _, s := range stats {
		weightedSum += s.AvgPerPod * int64(s.Samples)
		totalSamples += int64(s.Samples)
		out.MaxPerPod = maxInt64(out.MaxPerPod, s.MaxPerPod)
		out.P95PerPod = maxInt64(out.P95PerPod, s.P95PerPod)
		out.WindowSeconds = maxInt64(out.WindowSeconds, s.WindowSeconds)
	}
	if totalSamples > 0 {
		out.AvgPerPod = weightedSum / totalSamples
	}
	out.Samples = int(totalSamples)
	return &out
}

// sumUsage adds container-level usage into a workload-level per-pod view.
func sumUsage(containers []ContainerPacking, pick func(ContainerPacking) *UsageStats) *UsageStats {
	var out UsageStats
	found := false
	for _, c := range containers {
		s := pick(c)
		if s == nil {
			continue
		}
		found = true
		out.AvgPerPod += s.AvgPerPod
		out.MaxPerPod += s.MaxPerPod
		out.P95PerPod += s.P95PerPod
		if s.Samples > out.Samples {
			out.Samples = s.Samples
		}
		if s.WindowSeconds > out.WindowSeconds {
			out.WindowSeconds = s.WindowSeconds
		}
	}
	if !found {
		return nil
	}
	return &out
}

// packingContainers returns the containers whose requests occupy the pod's
// reservation for its lifetime: regular containers plus restartable init
// containers (native sidecars), which the scheduler also counts.
func packingContainers(pod *corev1.Pod) []*corev1.Container {
	out := make([]*corev1.Container, 0, len(pod.Spec.Containers))
	for i := range pod.Spec.Containers {
		out = append(out, &pod.Spec.Containers[i])
	}
	for i := range pod.Spec.InitContainers {
		c := &pod.Spec.InitContainers[i]
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			out = append(out, c)
		}
	}
	return out
}

func dominantQOS(counts map[string]int64) string {
	best := ""
	var bestN int64 = -1
	for _, q := range []string{"Guaranteed", "Burstable", "BestEffort"} {
		if counts[q] > bestN {
			best, bestN = q, counts[q]
		}
	}
	return best
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
