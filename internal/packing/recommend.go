package packing

import (
	"fmt"
	"sort"
	"strings"
)

// Thresholds for the right-sizing rules. Exported so tests and callers can
// see the exact policy; values are deliberately conservative.
const (
	// minUsageWindowSeconds gates usage-based rules on the actually observed
	// per-pod time span (the merged window is the longest single pod's span,
	// so replica count cannot weaken the gate).
	minUsageWindowSeconds = 300

	cpuHeadroom = 1.3  // suggested CPU request = p95 * headroom
	memHeadroom = 1.25 // suggested memory request = max * headroom

	overProvisionFactor = 0.5 // usage below half the request => oversized
	overcommitFactor    = 4   // limit >= 4x request => flagged

	// Reductions must be worth doing across the whole workload and
	// non-trivial per pod.
	minCPUSavingMilli = 25               // minimum total CPU saving
	minMemSavingBytes = 32 * 1024 * 1024 // minimum total memory saving (32Mi)
	minPerPodCPUMilli = 5                // minimum per-pod CPU delta
	minPerPodMemBytes = 8 * 1024 * 1024  // minimum per-pod memory delta (8Mi)

	floorCPUMilli = 10               // never suggest a request below 10m
	floorMemBytes = 16 * 1024 * 1024 // never suggest a request below 16Mi
)

// nodepoolLabels mirrors the frontend's pool grouping so node-shape analysis
// and the dashboard agree on pool names.
var nodepoolLabels = []string{
	"eks.amazonaws.com/nodegroup",
	"karpenter.sh/nodepool",
	"karpenter.sh/provisioner-name",
	"cloud.google.com/gke-nodepool",
	"lke.linode.com/pool-id",
	"kops.k8s.io/instancegroup",
	"node.kubernetes.io/nodepool",
	"agentpool",
	"nodepool",
}

// PoolOf returns the node pool name derived from well-known nodepool labels,
// or "(ungrouped)" when none match. Matches the dashboard's grouping.
func PoolOf(labels map[string]string) string {
	pool, _ := poolOfWithLabel(labels)
	return pool
}

func poolOfWithLabel(labels map[string]string) (pool, label string) {
	for _, l := range nodepoolLabels {
		if v := labels[l]; v != "" {
			return v, l
		}
	}
	return "(ungrouped)", ""
}

// ComputeRecommendations produces right-sizing recommendations for the given
// workloads. Usage-based rules fire only when a workload container has
// enough samples; spec-only rules always run. Pure and deterministic.
func ComputeRecommendations(workloads []WorkloadPacking) []Recommendation {
	var recs []Recommendation

	for _, w := range workloads {
		for _, c := range w.Containers {
			recs = append(recs, recommendContainer(w, c)...)
		}
	}

	// Highest severity first, then largest estimated savings.
	sevRank := map[string]int{"high": 0, "medium": 1, "low": 2}
	sort.SliceStable(recs, func(i, j int) bool {
		if sevRank[recs[i].Severity] != sevRank[recs[j].Severity] {
			return sevRank[recs[i].Severity] < sevRank[recs[j].Severity]
		}
		return savingsWeight(recs[i]) > savingsWeight(recs[j])
	})
	return recs
}

func recommendContainer(w WorkloadPacking, c ContainerPacking) []Recommendation {
	var recs []Recommendation

	base := Recommendation{Workload: w.WorkloadRef, Container: c.Name}
	cpuUse, memUse := c.CPU.Usage, c.Memory.Usage
	cpuUsable := cpuUse != nil && cpuUse.WindowSeconds >= minUsageWindowSeconds
	memUsable := memUse != nil && memUse.WindowSeconds >= minUsageWindowSeconds

	// 1. Missing requests (BestEffort-ish): the scheduler is flying blind.
	if c.CPU.RequestPerPod == 0 && c.Memory.RequestPerPod == 0 {
		r := base
		r.Type = "set-requests"
		r.Resource = "cpu+memory"
		r.Severity = "high"
		r.Rationale = "Container has no CPU or memory requests, so the scheduler places it blindly and it is first in line for eviction (BestEffort). Bin packing cannot account for it."
		if cpuUsable && memUsable {
			sugCPU := maxInt64(int64(float64(cpuUse.P95PerPod)*cpuHeadroom), floorCPUMilli)
			sugMem := maxInt64(int64(float64(memUse.MaxPerPod)*memHeadroom), floorMemBytes)
			r.CPURequest = &ResourceChange{Current: 0, Suggested: sugCPU}
			r.MemoryRequest = &ResourceChange{Current: 0, Suggested: sugMem}
			r.Basis = usageBasis(cpuUse)
			r.EstimatedSavings = &StrandedResources{CPUMillicores: -sugCPU * w.PodCount, MemoryBytes: -sugMem * w.PodCount}
			r.Action = setResourcesAction(w, c.Name, sugCPU, sugMem, 0, 0)
		} else {
			r.Basis = "spec-only; enable usage sampling (metrics-server) or observe the workload to pick values"
		}
		recs = append(recs, r)
		// Skip further rules for request-less containers: everything else
		// keys off requests.
		return recs
	}

	// 2. CPU request right-sizing from usage.
	if cpuUsable && c.CPU.RequestPerPod > 0 {
		req := c.CPU.RequestPerPod
		if float64(cpuUse.P95PerPod) < float64(req)*overProvisionFactor {
			suggested := maxInt64(int64(float64(cpuUse.P95PerPod)*cpuHeadroom), floorCPUMilli)
			perPod := req - suggested
			if perPod >= minPerPodCPUMilli && perPod*w.PodCount >= minCPUSavingMilli {
				r := base
				r.Type = "reduce-cpu-request"
				r.Resource = "cpu"
				r.Severity = severityBySavings(perPod*w.PodCount, 0)
				r.CPURequest = &ResourceChange{Current: req, Suggested: suggested}
				r.Rationale = fmt.Sprintf("p95 CPU usage is %dm against a %dm request (%.0f%% utilized); the reservation blocks capacity other pods could use.", cpuUse.P95PerPod, req, 100*float64(cpuUse.P95PerPod)/float64(req))
				r.Basis = usageBasis(cpuUse)
				r.EstimatedSavings = &StrandedResources{CPUMillicores: perPod * w.PodCount}
				r.Action = setResourcesAction(w, c.Name, suggested, 0, 0, 0)
				recs = append(recs, r)
			}
		} else if cpuUse.P95PerPod > req {
			suggested := int64(float64(cpuUse.P95PerPod) * cpuHeadroom)
			r := base
			r.Type = "increase-cpu-request"
			r.Resource = "cpu"
			r.Severity = "medium"
			r.CPURequest = &ResourceChange{Current: req, Suggested: suggested}
			r.Rationale = fmt.Sprintf("p95 CPU usage (%dm) exceeds the request (%dm); under node pressure this container will be throttled or starve neighbors it out-competes.", cpuUse.P95PerPod, req)
			r.Basis = usageBasis(cpuUse)
			r.EstimatedSavings = &StrandedResources{CPUMillicores: (req - suggested) * w.PodCount}
			var newLimit int64
			if c.CPU.LimitPerPod > 0 && suggested > c.CPU.LimitPerPod {
				// The raised request would exceed the current limit and the
				// apiserver rejects requests > limits: raise both together.
				newLimit = suggested
				r.CPULimit = &ResourceChange{Current: c.CPU.LimitPerPod, Suggested: newLimit}
			}
			r.Action = setResourcesAction(w, c.Name, suggested, 0, newLimit, 0)
			recs = append(recs, r)
		}
	}

	// 3. Memory request right-sizing from usage. Memory is not compressible,
	// so size against the observed max, not p95.
	if memUsable && c.Memory.RequestPerPod > 0 {
		req := c.Memory.RequestPerPod
		if float64(memUse.MaxPerPod) < float64(req)*overProvisionFactor {
			suggested := maxInt64(int64(float64(memUse.MaxPerPod)*memHeadroom), floorMemBytes)
			perPod := req - suggested
			if perPod >= minPerPodMemBytes && perPod*w.PodCount >= minMemSavingBytes {
				r := base
				r.Type = "reduce-memory-request"
				r.Resource = "memory"
				r.Severity = severityBySavings(0, perPod*w.PodCount)
				r.MemoryRequest = &ResourceChange{Current: req, Suggested: suggested}
				r.Rationale = fmt.Sprintf("Peak memory usage is %s against a %s request (%.0f%% utilized); the reservation strands node memory.", fmtBytes(memUse.MaxPerPod), fmtBytes(req), 100*float64(memUse.MaxPerPod)/float64(req))
				r.Basis = usageBasis(memUse)
				r.EstimatedSavings = &StrandedResources{MemoryBytes: perPod * w.PodCount}
				r.Action = setResourcesAction(w, c.Name, 0, suggested, 0, 0)
				recs = append(recs, r)
			}
		} else if memUse.MaxPerPod > req {
			suggested := maxInt64(int64(float64(memUse.MaxPerPod)*memHeadroom), floorMemBytes)
			r := base
			r.Type = "increase-memory-request"
			r.Resource = "memory"
			r.Severity = "high"
			r.MemoryRequest = &ResourceChange{Current: req, Suggested: suggested}
			r.Rationale = fmt.Sprintf("Peak memory usage (%s) exceeds the request (%s); on a full node this pod is a prime eviction target and can destabilize the node.", fmtBytes(memUse.MaxPerPod), fmtBytes(req))
			r.Basis = usageBasis(memUse)
			r.EstimatedSavings = &StrandedResources{MemoryBytes: (req - suggested) * w.PodCount}
			var newLimit int64
			if c.Memory.LimitPerPod > 0 && suggested > c.Memory.LimitPerPod {
				// Requests may not exceed limits; raise both together.
				newLimit = suggested
				r.MemoryLimit = &ResourceChange{Current: c.Memory.LimitPerPod, Suggested: newLimit}
			}
			r.Action = setResourcesAction(w, c.Name, 0, suggested, 0, newLimit)
			recs = append(recs, r)
		}
	}

	// 4. Memory limit sanity.
	if c.Memory.LimitPerPod > 0 && memUsable && float64(memUse.MaxPerPod) > 0.85*float64(c.Memory.LimitPerPod) {
		suggested := int64(float64(memUse.MaxPerPod) * 1.35)
		r := base
		r.Type = "raise-memory-limit"
		r.Resource = "memory"
		r.Severity = "high"
		r.MemoryLimit = &ResourceChange{Current: c.Memory.LimitPerPod, Suggested: suggested}
		r.Rationale = fmt.Sprintf("Peak memory usage (%s) is within 15%% of the limit (%s); the container risks OOM kills.", fmtBytes(memUse.MaxPerPod), fmtBytes(c.Memory.LimitPerPod))
		r.Basis = usageBasis(memUse)
		recs = append(recs, r)
	}
	if c.Memory.RequestPerPod > 0 && c.Memory.LimitPerPod == 0 {
		r := base
		r.Type = "set-memory-limit"
		r.Resource = "memory"
		r.Severity = "medium"
		r.Rationale = "Memory request is set but no limit; a leak in this container can consume the whole node before the kubelet reacts."
		r.Basis = "spec-only"
		if memUsable {
			r.MemoryLimit = &ResourceChange{Current: 0, Suggested: int64(float64(memUse.MaxPerPod) * 2)}
			r.Basis = usageBasis(memUse)
		}
		recs = append(recs, r)
	}

	// 5. Extreme limit overcommit (spec-only).
	if c.CPU.RequestPerPod > 0 && c.CPU.LimitPerPod >= overcommitFactor*c.CPU.RequestPerPod {
		r := base
		r.Type = "tighten-cpu-overcommit"
		r.Resource = "cpu"
		r.Severity = "low"
		r.Rationale = fmt.Sprintf("CPU limit (%dm) is %dx the request (%dm). Large gaps make node CPU contention unpredictable; either raise the request to what the container really needs or lower the limit.", c.CPU.LimitPerPod, c.CPU.LimitPerPod/c.CPU.RequestPerPod, c.CPU.RequestPerPod)
		r.Basis = "spec-only"
		recs = append(recs, r)
	}
	if c.Memory.RequestPerPod > 0 && c.Memory.LimitPerPod >= overcommitFactor*c.Memory.RequestPerPod {
		r := base
		r.Type = "tighten-memory-overcommit"
		r.Resource = "memory"
		r.Severity = "medium"
		r.Rationale = fmt.Sprintf("Memory limit (%s) is %dx the request (%s). Memory is incompressible: if containers burst toward their limits the node OOMs. Keep memory limit close to request.", fmtBytes(c.Memory.LimitPerPod), c.Memory.LimitPerPod/c.Memory.RequestPerPod, fmtBytes(c.Memory.RequestPerPod))
		r.Basis = "spec-only"
		recs = append(recs, r)
	}

	return recs
}

// ComputeNodeShapes analyzes each node pool's hardware shape against what
// workloads actually request on it, and suggests instance-type direction.
func ComputeNodeShapes(nodes []NodePacking) []NodeShapeAnalysis {
	type poolAgg struct {
		label     string
		nodes     []NodePacking
		instances map[string]struct{}
	}
	pools := make(map[string]*poolAgg)
	var order []string

	for _, n := range nodes {
		pool, label := poolOfWithLabel(n.Labels)
		p := pools[pool]
		if p == nil {
			p = &poolAgg{label: label, instances: make(map[string]struct{})}
			pools[pool] = p
			order = append(order, pool)
		}
		p.nodes = append(p.nodes, n)
		if it := n.Labels["node.kubernetes.io/instance-type"]; it != "" {
			p.instances[it] = struct{}{}
		}
	}
	sort.Strings(order)

	var out []NodeShapeAnalysis
	for _, poolName := range order {
		p := pools[poolName]
		var cpuAlloc, cpuReq, memAlloc, memReq int64
		for _, n := range p.nodes {
			cpuAlloc += n.CPU.Allocatable
			cpuReq += n.CPU.Requested
			memAlloc += n.Memory.Allocatable
			memReq += n.Memory.Requested
		}

		a := NodeShapeAnalysis{
			Pool:      poolName,
			PoolLabel: p.label,
			NodeCount: len(p.nodes),
		}
		for it := range p.instances {
			a.InstanceTypes = append(a.InstanceTypes, it)
		}
		sort.Strings(a.InstanceTypes)

		const gib = 1024 * 1024 * 1024
		if cpuAlloc > 0 {
			a.AllocatableGiBPerCore = float64(memAlloc) / gib / (float64(cpuAlloc) / 1000)
			a.CPURequestRatio = float64(cpuReq) / float64(cpuAlloc)
		}
		if cpuReq > 0 {
			a.RequestedGiBPerCore = float64(memReq) / gib / (float64(cpuReq) / 1000)
		}
		if memAlloc > 0 {
			a.MemRequestRatio = float64(memReq) / float64(memAlloc)
		}

		gap := a.CPURequestRatio - a.MemRequestRatio
		karpenter := p.label == "karpenter.sh/nodepool" || p.label == "karpenter.sh/provisioner-name"
		switch {
		case a.CPURequestRatio >= 0.7 && gap > 0.25:
			a.StrandedMemoryBytes = memAlloc - memReq
			a.Verdict = "cpu-bound"
			a.Suggestion = fmt.Sprintf("CPU requests fill %.0f%% of the pool while memory sits at %.0f%%: %s of memory is stranded. Workloads here request ~%.1f GiB/core but nodes provide %.1f GiB/core — move to a higher vCPU:memory shape (compute-optimized).", 100*a.CPURequestRatio, 100*a.MemRequestRatio, fmtBytes(a.StrandedMemoryBytes), a.RequestedGiBPerCore, a.AllocatableGiBPerCore)
			if karpenter {
				a.Suggestion += " With Karpenter, constrain the NodePool with requirements on karpenter.k8s.aws/instance-memory (lower) or instance-category (e.g. [\"c\"]) so provisioning matches the requested shape."
			}
		case a.MemRequestRatio >= 0.7 && gap < -0.25:
			a.StrandedCPUMillicores = cpuAlloc - cpuReq
			a.Verdict = "memory-bound"
			a.Suggestion = fmt.Sprintf("Memory requests fill %.0f%% of the pool while CPU sits at %.0f%%: %s of CPU is stranded. Workloads request ~%.1f GiB/core but nodes provide %.1f GiB/core — move to a higher memory:vCPU shape (memory-optimized).", 100*a.MemRequestRatio, 100*a.CPURequestRatio, fmtCores(a.StrandedCPUMillicores), a.RequestedGiBPerCore, a.AllocatableGiBPerCore)
			if karpenter {
				a.Suggestion += " With Karpenter, raise karpenter.k8s.aws/instance-memory or set instance-category to [\"r\"] in the NodePool requirements."
			}
		case a.CPURequestRatio < 0.4 && a.MemRequestRatio < 0.4 && len(p.nodes) > 1:
			a.Verdict = "under-utilized"
			a.Suggestion = fmt.Sprintf("Both CPU (%.0f%%) and memory (%.0f%%) requests are low across %d nodes — the pool can consolidate onto fewer or smaller nodes.", 100*a.CPURequestRatio, 100*a.MemRequestRatio, len(p.nodes))
			if karpenter {
				a.Suggestion += " Enable Karpenter consolidation (disruption: consolidationPolicy: WhenEmptyOrUnderutilized) to reclaim them automatically."
			}
		default:
			a.Verdict = "balanced"
			a.Suggestion = fmt.Sprintf("Requested shape (%.1f GiB/core) roughly matches the node shape (%.1f GiB/core); no instance-type change indicated.", a.RequestedGiBPerCore, a.AllocatableGiBPerCore)
		}

		out = append(out, a)
	}
	return out
}

func severityBySavings(cpuMilli, memBytes int64) string {
	if cpuMilli >= 500 || memBytes >= 512*1024*1024 {
		return "high"
	}
	if cpuMilli >= 100 || memBytes >= 128*1024*1024 {
		return "medium"
	}
	return "low"
}

func savingsWeight(r Recommendation) float64 {
	if r.EstimatedSavings == nil {
		return 0
	}
	// Normalize: 1 core ~ 4 GiB for ranking purposes.
	return float64(r.EstimatedSavings.CPUMillicores)/1000 + float64(r.EstimatedSavings.MemoryBytes)/(4*1024*1024*1024)
}

func usageBasis(s *UsageStats) string {
	return fmt.Sprintf("usage over %ds window, %d samples", s.WindowSeconds, s.Samples)
}

func setResourcesAction(w WorkloadPacking, container string, cpuReqMilli, memReqBytes, cpuLimMilli, memLimBytes int64) string {
	kindFlag := strings.ToLower(w.Kind)
	switch w.Kind {
	case "Deployment", "StatefulSet", "DaemonSet":
	default:
		return "" // no single-command action for jobs, bare pods, or custom kinds
	}
	var reqs []string
	if cpuReqMilli > 0 {
		reqs = append(reqs, fmt.Sprintf("cpu=%dm", cpuReqMilli))
	}
	if memReqBytes > 0 {
		reqs = append(reqs, fmt.Sprintf("memory=%dMi", ceilMi(memReqBytes)))
	}
	if len(reqs) == 0 {
		return ""
	}
	cmd := fmt.Sprintf("kubectl -n %s set resources %s/%s -c %s --requests=%s", w.Namespace, kindFlag, w.Name, container, strings.Join(reqs, ","))
	var lims []string
	if cpuLimMilli > 0 {
		lims = append(lims, fmt.Sprintf("cpu=%dm", cpuLimMilli))
	}
	if memLimBytes > 0 {
		lims = append(lims, fmt.Sprintf("memory=%dMi", ceilMi(memLimBytes)))
	}
	if len(lims) > 0 {
		cmd += " --limits=" + strings.Join(lims, ",")
	}
	return cmd
}

// ceilMi converts bytes to whole MiB, rounding up so a generated command
// never applies less than the suggested value (and never emits 0Mi).
func ceilMi(b int64) int64 {
	const mib = 1024 * 1024
	return (b + mib - 1) / mib
}

func fmtBytes(b int64) string {
	const gib = 1024 * 1024 * 1024
	const mib = 1024 * 1024
	if b >= gib {
		return fmt.Sprintf("%.1fGi", float64(b)/gib)
	}
	return fmt.Sprintf("%dMi", b/mib)
}

func fmtCores(milli int64) string {
	return fmt.Sprintf("%.1f cores", float64(milli)/1000)
}
