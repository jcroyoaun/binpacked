package packing

// ResourceValues holds the allocatable, requested, and limit values for a single resource.
type ResourceValues struct {
	Allocatable int64   `json:"allocatable"`
	Requested   int64   `json:"requested"`
	Limits      int64   `json:"limits"`
	Used        *int64  `json:"used,omitempty"`
	RequestRatio float64 `json:"requestRatio"`
}

// PodValues holds pod count and allocatable capacity for a node.
type PodValues struct {
	Allocatable int64   `json:"allocatable"`
	Count       int64   `json:"count"`
	Ratio       float64 `json:"ratio"`
}

// NodePacking holds computed bin packing data for a single node.
type NodePacking struct {
	Name               string            `json:"name"`
	Labels             map[string]string  `json:"labels"`
	CPU                ResourceValues    `json:"cpu"`
	Memory             ResourceValues    `json:"memory"`
	Pods               PodValues         `json:"pods"`
	Bottleneck         string            `json:"bottleneck"`
	BestEffortPodCount int64             `json:"bestEffortPodCount"`
	DaemonSetPodCount  int64             `json:"daemonSetPodCount"`
}

// PodInfo holds resource data for a single pod on a node.
type PodInfo struct {
	Name      string         `json:"name"`
	Namespace string         `json:"namespace"`
	CPU       ResourceValues `json:"cpu"`
	Memory    ResourceValues `json:"memory"`
	Phase     string         `json:"phase"`
	QOSClass  string         `json:"qosClass"`
	IsDaemonSet bool         `json:"isDaemonSet"`
}

// ClusterSummary holds aggregated cluster-wide packing data.
type ClusterSummary struct {
	TotalNodes int            `json:"totalNodes"`
	TotalPods  int            `json:"totalPods"`
	CPU        ResourceValues `json:"cpu"`
	Memory     ResourceValues `json:"memory"`
	Distribution map[string]int `json:"distribution"`
	StrandedResources StrandedResources `json:"strandedResources"`
	LeastPacked []NodePacking `json:"leastPacked"`
	MostPacked  []NodePacking `json:"mostPacked"`
}

// StrandedResources holds resources that are allocatable but unrequested.
type StrandedResources struct {
	CPUMillicores int64 `json:"cpuMillicores"`
	MemoryBytes   int64 `json:"memoryBytes"`
}
