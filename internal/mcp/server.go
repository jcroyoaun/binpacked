// Package mcp exposes binpacked's packing and right-sizing analysis as a
// Model Context Protocol server, so AI agents can query the cluster and act
// on the recommendations. Tools return the same shapes as the JSON API.
package mcp

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zillow/binpacked/internal/api"
	"github.com/zillow/binpacked/internal/packing"
)

const instructions = `binpacked reports Kubernetes bin-packing efficiency and right-sizing
recommendations computed from pod requests/limits and (when metrics-server is
available) observed usage. Units: CPU values are millicores (1000m = 1 core);
memory values are bytes. Ratios are 0-1 fractions of allocatable capacity.
Start with get_rightsizing_report for actionable findings; use list_workloads
and list_nodes to drill into the underlying data. Recommendations include
ready-to-run kubectl commands in their "action" field where applicable.`

// Deps are the dependencies the MCP tools call into. Computer and Usage are
// the same seams the REST API uses, so tool results match REST responses.
type Deps struct {
	Computer api.Computer
	Usage    api.UsageInfo
	Version  string
	Logger   *log.Logger
}

type emptyInput struct{}

type listNodesInput struct {
	Nodepool string `json:"nodepool,omitempty" jsonschema:"only return nodes in this node pool (pool name as shown by analyze_node_shapes)"`
	Sort     string `json:"sort,omitempty" jsonschema:"sort field: cpu_ratio (default), memory_ratio, pod_ratio, or name"`
	Order    string `json:"order,omitempty" jsonschema:"asc (default) or desc"`
}

type nodePodsInput struct {
	Node string `json:"node" jsonschema:"name of the node to list pods for"`
}

type listWorkloadsInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"only return workloads in this namespace"`
	Kind      string `json:"kind,omitempty" jsonschema:"only return workloads of this kind, e.g. Deployment, StatefulSet, DaemonSet"`
}

type rightsizingInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"restrict recommendations to workloads in this namespace (node-shape analysis stays cluster-wide)"`
}

type nodesOutput struct {
	Nodes []packing.NodePacking `json:"nodes"`
}

type nodePodsOutput struct {
	Node string            `json:"node"`
	Pods []packing.PodInfo `json:"pods"`
}

type workloadsOutput struct {
	Workloads []packing.WorkloadPacking `json:"workloads"`
}

type nodeShapesOutput struct {
	NodeShapes []packing.NodeShapeAnalysis `json:"nodeShapes"`
}

// NewServer builds the MCP server with all binpacked tools registered.
func NewServer(d Deps) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{
		Name:    "binpacked",
		Version: d.Version,
	}, &sdk.ServerOptions{
		Instructions: instructions,
	})

	sdk.AddTool(server, &sdk.Tool{
		Name:        "get_cluster_summary",
		Description: "Cluster-wide bin-packing summary: totals, request ratios, packing distribution across nodes, stranded (unrequestable) capacity, and the most/least packed nodes.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in emptyInput) (*sdk.CallToolResult, packing.ClusterSummary, error) {
		nodes, err := d.Computer.ComputeNodePacking()
		if err != nil {
			return nil, packing.ClusterSummary{}, err
		}
		return nil, packing.ComputeClusterSummary(nodes), nil
	})

	sdk.AddTool(server, &sdk.Tool{
		Name:        "list_nodes",
		Description: "Per-node packing data: allocatable vs requested CPU/memory/pods, request ratios, bottleneck resource, BestEffort and DaemonSet pod counts, and node labels (instance type, zone, pool).",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in listNodesInput) (*sdk.CallToolResult, nodesOutput, error) {
		nodes, err := d.Computer.ComputeNodePacking()
		if err != nil {
			return nil, nodesOutput{}, err
		}
		if in.Nodepool != "" {
			filtered := nodes[:0]
			for _, n := range nodes {
				if packing.PoolOf(n.Labels) == in.Nodepool {
					filtered = append(filtered, n)
				}
			}
			nodes = filtered
		}
		sortNodes(nodes, in.Sort, strings.EqualFold(in.Order, "desc"))
		return nil, nodesOutput{Nodes: nodes}, nil
	})

	sdk.AddTool(server, &sdk.Tool{
		Name:        "get_node_pods",
		Description: "All pods on one node with their CPU/memory requests and limits, phase, QoS class, and whether they belong to a DaemonSet.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in nodePodsInput) (*sdk.CallToolResult, nodePodsOutput, error) {
		if in.Node == "" {
			return nil, nodePodsOutput{}, fmt.Errorf("node is required")
		}
		pods, err := d.Computer.PodsOnNode(in.Node)
		if err != nil {
			return nil, nodePodsOutput{}, err
		}
		if pods == nil {
			pods = []packing.PodInfo{}
		}
		return nil, nodePodsOutput{Node: in.Node, Pods: pods}, nil
	})

	sdk.AddTool(server, &sdk.Tool{
		Name:        "list_workloads",
		Description: "Pods aggregated by owning workload (Deployment, StatefulSet, DaemonSet, CronJob, ...): per-pod and total requests/limits per container, QoS, node spread, and observed usage stats (avg/p95/max) when metrics-server is available.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in listWorkloadsInput) (*sdk.CallToolResult, workloadsOutput, error) {
		workloads, err := d.Computer.ComputeWorkloads()
		if err != nil {
			return nil, workloadsOutput{}, err
		}
		filtered := workloads[:0]
		for _, w := range workloads {
			if in.Namespace != "" && w.Namespace != in.Namespace {
				continue
			}
			if in.Kind != "" && !strings.EqualFold(w.Kind, in.Kind) {
				continue
			}
			filtered = append(filtered, w)
		}
		return nil, workloadsOutput{Workloads: filtered}, nil
	})

	sdk.AddTool(server, &sdk.Tool{
		Name:        "get_rightsizing_report",
		Description: "Actionable right-sizing report: per-container recommendations (set/reduce/increase requests, limit fixes) with severity, rationale, estimated savings, and ready-to-run kubectl commands; plus per-pool node-shape analysis (instance type direction, Karpenter hints, consolidation candidates). This is the best starting point for optimizing the cluster.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in rightsizingInput) (*sdk.CallToolResult, packing.RightsizingReport, error) {
		workloads, err := d.Computer.ComputeWorkloads()
		if err != nil {
			return nil, packing.RightsizingReport{}, err
		}
		nodes, err := d.Computer.ComputeNodePacking()
		if err != nil {
			return nil, packing.RightsizingReport{}, err
		}
		if in.Namespace != "" {
			filtered := workloads[:0]
			for _, w := range workloads {
				if w.Namespace == in.Namespace {
					filtered = append(filtered, w)
				}
			}
			workloads = filtered
		}
		return nil, api.BuildRightsizingReport(workloads, nodes, d.Usage), nil
	})

	sdk.AddTool(server, &sdk.Tool{
		Name:        "analyze_node_shapes",
		Description: "Per node pool: hardware shape (GiB per core) vs the shape workloads actually request, request ratios, stranded capacity, and a verdict (cpu-bound / memory-bound / under-utilized / balanced) with instance-type and Karpenter suggestions.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in emptyInput) (*sdk.CallToolResult, nodeShapesOutput, error) {
		nodes, err := d.Computer.ComputeNodePacking()
		if err != nil {
			return nil, nodeShapesOutput{}, err
		}
		return nil, nodeShapesOutput{NodeShapes: packing.ComputeNodeShapes(nodes)}, nil
	})

	return server
}

func sortNodes(nodes []packing.NodePacking, field string, desc bool) {
	sort.Slice(nodes, func(i, j int) bool {
		var less bool
		switch field {
		case "memory_ratio":
			less = nodes[i].Memory.RequestRatio < nodes[j].Memory.RequestRatio
		case "pod_ratio":
			less = nodes[i].Pods.Ratio < nodes[j].Pods.Ratio
		case "name":
			less = nodes[i].Name < nodes[j].Name
		default:
			less = nodes[i].CPU.RequestRatio < nodes[j].CPU.RequestRatio
		}
		if desc {
			return !less
		}
		return less
	})
}
