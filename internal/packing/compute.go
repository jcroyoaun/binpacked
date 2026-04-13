package packing

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	corelisters "k8s.io/client-go/listers/core/v1"
)

// Computer computes bin packing metrics from informer listers.
type Computer struct {
	NodeLister corelisters.NodeLister
	PodLister  corelisters.PodLister
}

// ComputeNodePacking computes packing data for all nodes.
func (c Computer) ComputeNodePacking() ([]NodePacking, error) {
	nodes, err := c.NodeLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}

	pods, err := c.PodLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	// Build a map of nodeName -> pods for fast lookup.
	podsByNode := make(map[string][]*corev1.Pod, len(nodes))
	for _, pod := range pods {
		if pod.Spec.NodeName == "" {
			continue
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		podsByNode[pod.Spec.NodeName] = append(podsByNode[pod.Spec.NodeName], pod)
	}

	result := make([]NodePacking, 0, len(nodes))
	for _, node := range nodes {
		np := ComputeForNode(node, podsByNode[node.Name])
		result = append(result, np)
	}
	return result, nil
}

// ComputeClusterSummary computes the cluster-level summary from per-node data.
func ComputeClusterSummary(nodes []NodePacking) ClusterSummary {
	var s ClusterSummary
	s.TotalNodes = len(nodes)
	s.Distribution = map[string]int{
		"0-25":   0,
		"25-50":  0,
		"50-75":  0,
		"75-90":  0,
		"90-100": 0,
	}

	for _, n := range nodes {
		s.CPU.Allocatable += n.CPU.Allocatable
		s.CPU.Requested += n.CPU.Requested
		s.CPU.Limits += n.CPU.Limits
		s.Memory.Allocatable += n.Memory.Allocatable
		s.Memory.Requested += n.Memory.Requested
		s.Memory.Limits += n.Memory.Limits
		s.TotalPods += int(n.Pods.Count)

		// Distribution uses the dominant (max) packing ratio.
		dominant := maxRatio(n)
		switch {
		case dominant >= 0.90:
			s.Distribution["90-100"]++
		case dominant >= 0.75:
			s.Distribution["75-90"]++
		case dominant >= 0.50:
			s.Distribution["50-75"]++
		case dominant >= 0.25:
			s.Distribution["25-50"]++
		default:
			s.Distribution["0-25"]++
		}

		// Stranded resources: if a node is bottlenecked on one resource,
		// the headroom on the other resource is "stranded."
		if n.Bottleneck == "cpu" || n.Bottleneck == "pods" {
			s.StrandedResources.MemoryBytes += n.Memory.Allocatable - n.Memory.Requested
		}
		if n.Bottleneck == "memory" || n.Bottleneck == "pods" {
			s.StrandedResources.CPUMillicores += n.CPU.Allocatable - n.CPU.Requested
		}
	}

	if s.CPU.Allocatable > 0 {
		s.CPU.RequestRatio = float64(s.CPU.Requested) / float64(s.CPU.Allocatable)
	}
	if s.Memory.Allocatable > 0 {
		s.Memory.RequestRatio = float64(s.Memory.Requested) / float64(s.Memory.Allocatable)
	}

	// Least and most packed (by dominant ratio), up to 5 each.
	sorted := make([]NodePacking, len(nodes))
	copy(sorted, nodes)
	sort.Slice(sorted, func(i, j int) bool {
		return maxRatio(sorted[i]) < maxRatio(sorted[j])
	})

	top := 5
	if len(sorted) < top {
		top = len(sorted)
	}
	s.LeastPacked = sorted[:top]

	mostStart := len(sorted) - top
	most := make([]NodePacking, top)
	copy(most, sorted[mostStart:])
	// Reverse so the most packed is first.
	for i, j := 0, len(most)-1; i < j; i, j = i+1, j-1 {
		most[i], most[j] = most[j], most[i]
	}
	s.MostPacked = most

	return s
}

// PodsOnNode returns pod info for all pods on the given node.
func (c Computer) PodsOnNode(nodeName string) ([]PodInfo, error) {
	pods, err := c.PodLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("listing pods for node %q: %w", nodeName, err)
	}

	var result []PodInfo
	for _, pod := range pods {
		if pod.Spec.NodeName != nodeName {
			continue
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		result = append(result, PodToInfo(pod))
	}
	return result, nil
}

// ComputeForNode computes packing data for a single node given its pods.
// This is the pure computation function — no informer dependency.
func ComputeForNode(node *corev1.Node, pods []*corev1.Pod) NodePacking {
	alloc := node.Status.Allocatable

	var np NodePacking
	np.Name = node.Name
	np.Labels = node.Labels
	np.CPU.Allocatable = alloc.Cpu().MilliValue()
	np.Memory.Allocatable = alloc.Memory().Value()
	np.Pods.Allocatable = alloc.Pods().Value()

	for _, pod := range pods {
		np.Pods.Count++

		if isDaemonSetPod(pod) {
			np.DaemonSetPodCount++
		}

		if qosClass(pod) == "BestEffort" {
			np.BestEffortPodCount++
		}

		for i := range pod.Spec.Containers {
			c := &pod.Spec.Containers[i]
			np.CPU.Requested += c.Resources.Requests.Cpu().MilliValue()
			np.CPU.Limits += c.Resources.Limits.Cpu().MilliValue()
			np.Memory.Requested += c.Resources.Requests.Memory().Value()
			np.Memory.Limits += c.Resources.Limits.Memory().Value()
		}

		// Include init container requests (K8s uses the max of init vs sum of regular).
		// For simplicity in the packing view, we only sum regular containers since that's
		// what's actively consuming the requested reservation after startup.
	}

	if np.CPU.Allocatable > 0 {
		np.CPU.RequestRatio = float64(np.CPU.Requested) / float64(np.CPU.Allocatable)
	}
	if np.Memory.Allocatable > 0 {
		np.Memory.RequestRatio = float64(np.Memory.Requested) / float64(np.Memory.Allocatable)
	}
	if np.Pods.Allocatable > 0 {
		np.Pods.Ratio = float64(np.Pods.Count) / float64(np.Pods.Allocatable)
	}

	// Determine bottleneck: whichever resource has the highest request ratio.
	np.Bottleneck = "cpu"
	highest := np.CPU.RequestRatio
	if np.Memory.RequestRatio > highest {
		np.Bottleneck = "memory"
		highest = np.Memory.RequestRatio
	}
	if np.Pods.Ratio > highest {
		np.Bottleneck = "pods"
	}

	return np
}

func isDaemonSetPod(pod *corev1.Pod) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

func qosClass(pod *corev1.Pod) string {
	if pod.Status.QOSClass != "" {
		return string(pod.Status.QOSClass)
	}
	// Compute if not set in status.
	hasRequests := false
	allHaveLimits := true
	allMatch := true
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		req := c.Resources.Requests
		lim := c.Resources.Limits
		if req.Cpu().MilliValue() > 0 || req.Memory().Value() > 0 {
			hasRequests = true
		}
		if lim.Cpu().IsZero() || lim.Memory().IsZero() {
			allHaveLimits = false
		}
		if !lim.Cpu().Equal(*req.Cpu()) || !lim.Memory().Equal(*req.Memory()) {
			allMatch = false
		}
	}
	if !hasRequests && !allHaveLimits {
		return "BestEffort"
	}
	if allHaveLimits && allMatch {
		return "Guaranteed"
	}
	return "Burstable"
}

// PodToInfo converts a Kubernetes Pod to a PodInfo summary.
func PodToInfo(pod *corev1.Pod) PodInfo {
	var info PodInfo
	info.Name = pod.Name
	info.Namespace = pod.Namespace
	info.Phase = string(pod.Status.Phase)
	info.QOSClass = qosClass(pod)
	info.IsDaemonSet = isDaemonSetPod(pod)

	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		info.CPU.Requested += c.Resources.Requests.Cpu().MilliValue()
		info.CPU.Limits += c.Resources.Limits.Cpu().MilliValue()
		info.Memory.Requested += c.Resources.Requests.Memory().Value()
		info.Memory.Limits += c.Resources.Limits.Memory().Value()
	}
	return info
}

func maxRatio(n NodePacking) float64 {
	m := n.CPU.RequestRatio
	if n.Memory.RequestRatio > m {
		m = n.Memory.RequestRatio
	}
	if n.Pods.Ratio > m {
		m = n.Pods.Ratio
	}
	return m
}
