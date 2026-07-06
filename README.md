# binpacked

Kubernetes bin-packing dashboard and right-sizing analyzer, styled after the
official Kubernetes Dashboard. Visualizes how pod resource requests pack onto
nodes, aggregates pods by owning workload, samples real usage from
metrics-server, and produces actionable right-sizing recommendations — for
humans in the UI and for AI agents over MCP and JSON.

## Deploy

```sh
kubectl apply -f deploy/quickstart.yaml
kubectl -n binpacked rollout status deployment/binpacked
kubectl -n binpacked port-forward svc/binpacked 8080:8080
# dashboard: http://localhost:8080
```

## Run locally

```sh
go run . -kubeconfig ~/.kube/config
```

Flags: `-addr` (default `:8080`), `-kubeconfig`, `-metrics-interval` (usage
sampling period, default `30s`, `0` disables), `-metrics-window` (samples kept
per container, default `120`), `-mcp-stdio` (serve MCP on stdio instead of
HTTP).

## Using with AI agents

binpacked is built to be driven by agents. Full machine-readable docs are
served at `/llms.txt`.

### MCP

Streamable HTTP endpoint at `/mcp` (same port as the dashboard):

```sh
claude mcp add --transport http binpacked http://localhost:8080/mcp
```

Or stdio for local use without the HTTP server:

```sh
claude mcp add --transport stdio binpacked -- \
  binpacked -mcp-stdio -kubeconfig ~/.kube/config
```

Tools: `get_rightsizing_report` (start here), `list_workloads`,
`get_cluster_summary`, `list_nodes`, `get_node_pods`, `analyze_node_shapes`.

Recommendations include severity, rationale, the data basis (spec-only vs
usage over a stated window), estimated savings, and ready-to-run `kubectl`
commands. Node-shape analysis flags cpu-bound / memory-bound / under-utilized
pools with instance-type direction and Karpenter NodePool hints.

### JSON API

```
GET /api/v1/cluster-summary
GET /api/v1/nodes?nodepool=&sort=cpu_ratio|memory_ratio|pod_ratio|name&order=desc
GET /api/v1/nodes/{name}/pods
GET /api/v1/workloads?namespace=&kind=
GET /api/v1/rightsizing?namespace=
GET /api/v1/node-shapes
GET /api/v1/health
```

Units everywhere: CPU in millicores, memory in bytes, ratios as 0-1 fractions
of allocatable.

## How recommendations work

Spec-only rules always run: missing requests (BestEffort), missing memory
limits, extreme limit:request overcommit. When metrics-server is available,
the sampler keeps a rolling window of per-container usage and adds
usage-based rules: reduce/increase CPU requests against p95, reduce/increase
memory requests against observed peaks, and OOM-risk warnings when peaks
approach limits. RBAC for `metrics.k8s.io` is included in the quickstart;
without it the report degrades to spec-only and says so in `notes`.
