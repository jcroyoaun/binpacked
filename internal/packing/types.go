package packing

// ResourceValues holds the allocatable, requested, and limit values for a single resource.
type ResourceValues struct {
	Allocatable  int64   `json:"allocatable"`
	Requested    int64   `json:"requested"`
	Limits       int64   `json:"limits"`
	Used         *int64  `json:"used,omitempty"`
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
	Labels             map[string]string `json:"labels"`
	CPU                ResourceValues    `json:"cpu"`
	Memory             ResourceValues    `json:"memory"`
	Pods               PodValues         `json:"pods"`
	Bottleneck         string            `json:"bottleneck"`
	BestEffortPodCount int64             `json:"bestEffortPodCount"`
	DaemonSetPodCount  int64             `json:"daemonSetPodCount"`
}

// PodInfo holds resource data for a single pod on a node.
type PodInfo struct {
	Name        string         `json:"name"`
	Namespace   string         `json:"namespace"`
	CPU         ResourceValues `json:"cpu"`
	Memory      ResourceValues `json:"memory"`
	Phase       string         `json:"phase"`
	QOSClass    string         `json:"qosClass"`
	IsDaemonSet bool           `json:"isDaemonSet"`
}

// ClusterSummary holds aggregated cluster-wide packing data.
type ClusterSummary struct {
	TotalNodes        int               `json:"totalNodes"`
	TotalPods         int               `json:"totalPods"`
	CPU               ResourceValues    `json:"cpu"`
	Memory            ResourceValues    `json:"memory"`
	Distribution      map[string]int    `json:"distribution"`
	StrandedResources StrandedResources `json:"strandedResources"`
	LeastPacked       []NodePacking     `json:"leastPacked"`
	MostPacked        []NodePacking     `json:"mostPacked"`
}

// StrandedResources holds resources that are allocatable but unrequested.
type StrandedResources struct {
	CPUMillicores int64 `json:"cpuMillicores"`
	MemoryBytes   int64 `json:"memoryBytes"`
}

// WorkloadRef identifies a workload (the controller owning a set of pods).
type WorkloadRef struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
}

// UsageStats summarizes observed usage samples for one resource of one
// workload container across all of its pods. CPU values are millicores,
// memory values are bytes (working set).
type UsageStats struct {
	AvgPerPod     int64 `json:"avgPerPod"`
	MaxPerPod     int64 `json:"maxPerPod"`
	P95PerPod     int64 `json:"p95PerPod"`
	Samples       int   `json:"samples"`
	WindowSeconds int64 `json:"windowSeconds"`
}

// WorkloadResource holds per-pod and total request/limit values for one
// resource of a workload, plus observed usage when metrics are available.
type WorkloadResource struct {
	RequestPerPod int64       `json:"requestPerPod"`
	LimitPerPod   int64       `json:"limitPerPod"`
	RequestTotal  int64       `json:"requestTotal"`
	LimitTotal    int64       `json:"limitTotal"`
	Usage         *UsageStats `json:"usage,omitempty"`
}

// ContainerPacking holds per-container aggregates for a workload. Requests
// and limits are taken from the pod spec (identical across replicas in the
// common case; if replicas disagree the max is reported).
type ContainerPacking struct {
	Name   string           `json:"name"`
	CPU    WorkloadResource `json:"cpu"`
	Memory WorkloadResource `json:"memory"`
}

// WorkloadPacking aggregates all running pods owned by one workload.
type WorkloadPacking struct {
	WorkloadRef
	PodCount int64 `json:"podCount"`
	// PendingPods counts pods not yet scheduled to a node; they are included
	// in PodCount and the request totals.
	PendingPods int64              `json:"pendingPods,omitempty"`
	QOSClass    string             `json:"qosClass"`
	IsDaemonSet bool               `json:"isDaemonSet"`
	CPU         WorkloadResource   `json:"cpu"`
	Memory      WorkloadResource   `json:"memory"`
	Containers  []ContainerPacking `json:"containers"`
	Nodes       []string           `json:"nodes"`
}

// ResourceChange describes a current and suggested value for one resource
// of one container. Zero Suggested with Set=false means "no change".
type ResourceChange struct {
	Current   int64 `json:"current"`
	Suggested int64 `json:"suggested"`
}

// Recommendation is a single actionable right-sizing finding.
type Recommendation struct {
	Workload  WorkloadRef `json:"workload"`
	Container string      `json:"container,omitempty"`
	Type      string      `json:"type"`
	Resource  string      `json:"resource"`
	Severity  string      `json:"severity"`
	Rationale string      `json:"rationale"`
	Basis     string      `json:"basis"`

	CPURequest    *ResourceChange `json:"cpuRequest,omitempty"`
	MemoryRequest *ResourceChange `json:"memoryRequest,omitempty"`
	MemoryLimit   *ResourceChange `json:"memoryLimit,omitempty"`
	CPULimit      *ResourceChange `json:"cpuLimit,omitempty"`

	// Estimated total change across all replicas if the suggestion is
	// applied. Positive values free capacity; negative values consume more.
	EstimatedSavings *StrandedResources `json:"estimatedSavings,omitempty"`

	// Action is a ready-to-run kubectl command implementing the suggestion,
	// when one exists.
	Action string `json:"action,omitempty"`
}

// NodeShapeAnalysis compares a node pool's hardware shape against the shape
// of the workloads requested onto it.
type NodeShapeAnalysis struct {
	Pool                  string   `json:"pool"`
	PoolLabel             string   `json:"poolLabel,omitempty"`
	NodeCount             int      `json:"nodeCount"`
	InstanceTypes         []string `json:"instanceTypes,omitempty"`
	AllocatableGiBPerCore float64  `json:"allocatableGiBPerCore"`
	RequestedGiBPerCore   float64  `json:"requestedGiBPerCore"`
	CPURequestRatio       float64  `json:"cpuRequestRatio"`
	MemRequestRatio       float64  `json:"memRequestRatio"`
	StrandedCPUMillicores int64    `json:"strandedCpuMillicores"`
	StrandedMemoryBytes   int64    `json:"strandedMemoryBytes"`
	Verdict               string   `json:"verdict"`
	Suggestion            string   `json:"suggestion"`
}

// RightsizingReport is the full agent-facing report.
type RightsizingReport struct {
	GeneratedAt        string              `json:"generatedAt"`
	MetricsAvailable   bool                `json:"metricsAvailable"`
	UsageWindowSeconds int64               `json:"usageWindowSeconds,omitempty"`
	WorkloadCount      int                 `json:"workloadCount"`
	Recommendations    []Recommendation    `json:"recommendations"`
	NodeShapes         []NodeShapeAnalysis `json:"nodeShapes"`
	// TotalPotentialSavings sums only capacity that reductions would free.
	TotalPotentialSavings StrandedResources `json:"totalPotentialSavings"`
	// AdditionalCapacityNeeded sums the extra capacity that corrective
	// increases (set-requests, increase-*) would reserve. Kept separate from
	// savings: netting the two would hide both.
	AdditionalCapacityNeeded StrandedResources `json:"additionalCapacityNeeded"`
	Notes                    []string          `json:"notes,omitempty"`
}
