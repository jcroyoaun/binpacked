package api

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/zillow/binpacked/internal/packing"
)

// Computer describes the packing operations the API layer depends on.
type Computer interface {
	ComputeNodePacking() ([]packing.NodePacking, error)
	PodsOnNode(nodeName string) ([]packing.PodInfo, error)
}

// Handler serves the bin packing JSON API.
type Handler struct {
	Computer Computer
	Logger   *log.Logger
}

func (h Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/health", h.health)
	mux.HandleFunc("GET /api/v1/cluster-summary", h.clusterSummary)
	mux.HandleFunc("GET /api/v1/nodes", h.nodes)
	mux.HandleFunc("GET /api/v1/nodes/{name}/pods", h.nodePods)
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

	// Filter by nodepool label.
	if np := r.URL.Query().Get("nodepool"); np != "" {
		filtered := nodes[:0]
		for _, n := range nodes {
			if n.Labels["nodepool"] == np || n.Labels["node.kubernetes.io/nodepool"] == np {
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
