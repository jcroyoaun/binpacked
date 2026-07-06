package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zillow/binpacked/internal/packing"
)

// Computer describes the packing operations the API layer depends on.
type Computer interface {
	ComputeNodePacking() ([]packing.NodePacking, error)
	PodsOnNode(nodeName string) ([]packing.PodInfo, error)
	ComputeWorkloads() ([]packing.WorkloadPacking, error)
}

// UsageInfo reports the state of the optional metrics sampler.
type UsageInfo interface {
	Available() bool
	Window() int64
}

// Handler serves the bin packing JSON API.
type Handler struct {
	Computer Computer
	// Usage is optional; nil means no metrics sampler is running.
	Usage  UsageInfo
	Logger *log.Logger
}

func (h Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/health", h.health)
	mux.HandleFunc("GET /api/v1/cluster-summary", h.clusterSummary)
	mux.HandleFunc("GET /api/v1/nodes", h.nodes)
	mux.HandleFunc("GET /api/v1/nodes/{name}/pods", h.nodePods)
	mux.HandleFunc("GET /api/v1/workloads", h.workloads)
	mux.HandleFunc("GET /api/v1/rightsizing", h.rightsizing)
	mux.HandleFunc("GET /api/v1/node-shapes", h.nodeShapes)
}

func (h Handler) health(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h Handler) clusterSummary(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.Computer.ComputeNodePacking()
	if err != nil {
		h.logger().Printf("cluster-summary: compute node packing: %v", err)
		h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	summary := packing.ComputeClusterSummary(nodes)
	h.logger().Printf("cluster-summary: nodes=%d pods=%d node_names=%v", len(nodes), summary.TotalPods, nodeNames(nodes))
	h.writeJSON(w, http.StatusOK, summary)
}

func (h Handler) nodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.Computer.ComputeNodePacking()
	if err != nil {
		h.logger().Printf("nodes: compute node packing: query=%q: %v", r.URL.RawQuery, err)
		h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Filter by node pool, using the same well-known-label resolution the
	// rest of the product (dashboard grouping, MCP, node-shape analysis) uses.
	if np := r.URL.Query().Get("nodepool"); np != "" {
		filtered := nodes[:0]
		for _, n := range nodes {
			if packing.PoolOf(n.Labels) == np {
				filtered = append(filtered, n)
			}
		}
		nodes = filtered
	}

	// Sort.
	sortField := r.URL.Query().Get("sort")
	if sortField == "" {
		sortField = "cpu_ratio"
	}
	desc := strings.ToLower(r.URL.Query().Get("order")) == "desc"

	sort.Slice(nodes, func(i, j int) bool {
		var less bool
		switch sortField {
		case "memory_ratio":
			less = nodes[i].Memory.RequestRatio < nodes[j].Memory.RequestRatio
		case "pod_ratio":
			less = nodes[i].Pods.Ratio < nodes[j].Pods.Ratio
		case "name":
			less = nodes[i].Name < nodes[j].Name
		default: // cpu_ratio
			less = nodes[i].CPU.RequestRatio < nodes[j].CPU.RequestRatio
		}
		if desc {
			return !less
		}
		return less
	})

	h.logger().Printf("nodes: query=%q count=%d node_names=%v", r.URL.RawQuery, len(nodes), nodeNames(nodes))
	h.writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

func (h Handler) nodePods(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node name required"})
		return
	}

	pods, err := h.Computer.PodsOnNode(name)
	if err != nil {
		h.logger().Printf("node-pods: node=%q: %v", name, err)
		h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	h.logger().Printf("node-pods: node=%q count=%d", name, len(pods))
	h.writeJSON(w, http.StatusOK, map[string]any{"node": name, "pods": pods})
}

func (h Handler) workloads(w http.ResponseWriter, r *http.Request) {
	workloads, err := h.Computer.ComputeWorkloads()
	if err != nil {
		h.logger().Printf("workloads: compute workloads: query=%q: %v", r.URL.RawQuery, err)
		h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if ns := r.URL.Query().Get("namespace"); ns != "" {
		filtered := workloads[:0]
		for _, wl := range workloads {
			if wl.Namespace == ns {
				filtered = append(filtered, wl)
			}
		}
		workloads = filtered
	}
	if kind := r.URL.Query().Get("kind"); kind != "" {
		filtered := workloads[:0]
		for _, wl := range workloads {
			if strings.EqualFold(wl.Kind, kind) {
				filtered = append(filtered, wl)
			}
		}
		workloads = filtered
	}

	h.logger().Printf("workloads: query=%q count=%d", r.URL.RawQuery, len(workloads))
	h.writeJSON(w, http.StatusOK, map[string]any{"workloads": workloads})
}

func (h Handler) rightsizing(w http.ResponseWriter, r *http.Request) {
	workloads, err := h.Computer.ComputeWorkloads()
	if err != nil {
		h.logger().Printf("rightsizing: compute workloads: %v", err)
		h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	nodes, err := h.Computer.ComputeNodePacking()
	if err != nil {
		h.logger().Printf("rightsizing: compute node packing: %v", err)
		h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if ns := r.URL.Query().Get("namespace"); ns != "" {
		filtered := workloads[:0]
		for _, wl := range workloads {
			if wl.Namespace == ns {
				filtered = append(filtered, wl)
			}
		}
		workloads = filtered
	}

	report := BuildRightsizingReport(workloads, nodes, h.Usage)
	h.logger().Printf("rightsizing: query=%q workloads=%d recommendations=%d", r.URL.RawQuery, report.WorkloadCount, len(report.Recommendations))
	h.writeJSON(w, http.StatusOK, report)
}

func (h Handler) nodeShapes(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.Computer.ComputeNodePacking()
	if err != nil {
		h.logger().Printf("node-shapes: compute node packing: %v", err)
		h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	shapes := packing.ComputeNodeShapes(nodes)
	h.logger().Printf("node-shapes: pools=%d", len(shapes))
	h.writeJSON(w, http.StatusOK, map[string]any{"nodeShapes": shapes})
}

// BuildRightsizingReport assembles the full agent-facing report. Shared by
// the REST route and the MCP tool so both return identical shapes.
func BuildRightsizingReport(workloads []packing.WorkloadPacking, nodes []packing.NodePacking, usage UsageInfo) packing.RightsizingReport {
	report := packing.RightsizingReport{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		WorkloadCount:   len(workloads),
		Recommendations: packing.ComputeRecommendations(workloads),
		NodeShapes:      packing.ComputeNodeShapes(nodes),
	}
	if usage != nil && usage.Available() {
		report.MetricsAvailable = true
		report.UsageWindowSeconds = usage.Window()
	} else {
		report.Notes = append(report.Notes, "metrics-server data unavailable: recommendations are spec-only; usage-based right-sizing needs the metrics API")
	}
	if report.Recommendations == nil {
		report.Recommendations = []packing.Recommendation{}
	}
	for _, rec := range report.Recommendations {
		if rec.EstimatedSavings == nil {
			continue
		}
		// Positive values free capacity; negative values reserve more.
		// Accumulate the two directions separately per resource.
		if cpu := rec.EstimatedSavings.CPUMillicores; cpu > 0 {
			report.TotalPotentialSavings.CPUMillicores += cpu
		} else {
			report.AdditionalCapacityNeeded.CPUMillicores += -cpu
		}
		if mem := rec.EstimatedSavings.MemoryBytes; mem > 0 {
			report.TotalPotentialSavings.MemoryBytes += mem
		} else {
			report.AdditionalCapacityNeeded.MemoryBytes += -mem
		}
	}
	var pending int64
	for _, wl := range workloads {
		pending += wl.PendingPods
	}
	if pending > 0 {
		report.Notes = append(report.Notes, fmt.Sprintf("%d pods are pending (unscheduled); workload totals and estimated savings include their requested footprint", pending))
	}
	return report
}

func (h Handler) writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		h.logger().Printf("encode json response: status=%d: %v", status, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body = append(body, '\n')
	if _, err := bytes.NewBuffer(body).WriteTo(w); err != nil {
		h.logger().Printf("write json response: status=%d: %v", status, err)
	}
}

func (h Handler) logger() *log.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return log.Default()
}

func nodeNames(nodes []packing.NodePacking) []string {
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		names = append(names, node.Name)
	}
	sort.Strings(names)
	return names
}
