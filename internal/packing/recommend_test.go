package packing_test

import (
	"strings"
	"testing"

	"github.com/zillow/binpacked/internal/packing"
)

const mib = 1024 * 1024

func usageStats(avg, p95, max int64, samples int) *packing.UsageStats {
	return &packing.UsageStats{AvgPerPod: avg, P95PerPod: p95, MaxPerPod: max, Samples: samples, WindowSeconds: 1800}
}

func workload(kind, namespace, name string, podCount int64, c packing.ContainerPacking) packing.WorkloadPacking {
	return packing.WorkloadPacking{
		WorkloadRef: packing.WorkloadRef{Namespace: namespace, Kind: kind, Name: name},
		PodCount:    podCount,
		Containers:  []packing.ContainerPacking{c},
	}
}

func wres(reqPerPod, limPerPod, podCount int64, usage *packing.UsageStats) packing.WorkloadResource {
	return packing.WorkloadResource{
		RequestPerPod: reqPerPod,
		LimitPerPod:   limPerPod,
		RequestTotal:  reqPerPod * podCount,
		LimitTotal:    limPerPod * podCount,
		Usage:         usage,
	}
}

func findRec(recs []packing.Recommendation, recType string) *packing.Recommendation {
	for i := range recs {
		if recs[i].Type == recType {
			return &recs[i]
		}
	}
	return nil
}

func TestComputeRecommendationsSetRequests(t *testing.T) {
	t.Parallel()

	w := workload("Deployment", "prod", "web", 3, packing.ContainerPacking{
		Name:   "main",
		CPU:    wres(0, 0, 3, usageStats(20, 40, 45, 60)),
		Memory: wres(0, 0, 3, usageStats(50*mib, 58*mib, 60*mib, 60)),
	})

	recs := packing.ComputeRecommendations([]packing.WorkloadPacking{w})
	rec := findRec(recs, "set-requests")
	if rec == nil {
		t.Fatal("expected a set-requests recommendation")
	}
	if rec.Severity != "high" {
		t.Log("exp:", "high")
		t.Log("got:", rec.Severity)
		t.Fatal("severity mismatch")
	}
	// CPU: p95(40) * 1.3 = 52m; memory: max(60Mi) * 1.25 = 75Mi.
	if rec.CPURequest == nil || rec.CPURequest.Suggested != 52 {
		t.Log("exp:", 52)
		t.Log("got:", rec.CPURequest)
		t.Fatal("suggested cpu request mismatch")
	}
	if rec.MemoryRequest == nil || rec.MemoryRequest.Suggested != 75*mib {
		t.Log("exp:", 75*mib)
		t.Log("got:", rec.MemoryRequest)
		t.Fatal("suggested memory request mismatch")
	}
	if !strings.Contains(rec.Action, "kubectl -n prod set resources deployment/web -c main") {
		t.Log("got:", rec.Action)
		t.Fatal("action command mismatch")
	}
}

func TestComputeRecommendationsReduceCPU(t *testing.T) {
	t.Parallel()

	// Request 1000m, p95 only 100m => suggest 130m, saving 870m/pod.
	w := workload("Deployment", "prod", "api", 4, packing.ContainerPacking{
		Name:   "main",
		CPU:    wres(1000, 2000, 4, usageStats(80, 100, 120, 60)),
		Memory: wres(256*mib, 512*mib, 4, usageStats(100*mib, 150*mib, 180*mib, 60)),
	})

	recs := packing.ComputeRecommendations([]packing.WorkloadPacking{w})
	rec := findRec(recs, "reduce-cpu-request")
	if rec == nil {
		t.Fatal("expected a reduce-cpu-request recommendation")
	}
	if rec.CPURequest.Suggested != 130 {
		t.Log("exp:", 130)
		t.Log("got:", rec.CPURequest.Suggested)
		t.Fatal("suggested cpu mismatch")
	}
	if rec.EstimatedSavings == nil || rec.EstimatedSavings.CPUMillicores != (1000-130)*4 {
		t.Log("exp:", (1000-130)*4)
		t.Log("got:", rec.EstimatedSavings)
		t.Fatal("estimated savings mismatch")
	}
	// 3480m saved >= 500m => high severity, and it must sort first.
	if rec.Severity != "high" {
		t.Log("exp:", "high")
		t.Log("got:", rec.Severity)
		t.Fatal("severity mismatch")
	}
	if recs[0].Type != "reduce-cpu-request" {
		t.Log("exp:", "reduce-cpu-request first")
		t.Log("got:", recs[0].Type)
		t.Fatal("ordering mismatch")
	}
}

func TestComputeRecommendationsUnderProvisioned(t *testing.T) {
	t.Parallel()

	// Memory max (300Mi) above request (200Mi) => increase, high severity.
	w := workload("StatefulSet", "prod", "db", 2, packing.ContainerPacking{
		Name:   "postgres",
		CPU:    wres(500, 0, 2, usageStats(300, 450, 480, 60)),
		Memory: wres(200*mib, 400*mib, 2, usageStats(250*mib, 280*mib, 300*mib, 60)),
	})

	recs := packing.ComputeRecommendations([]packing.WorkloadPacking{w})
	rec := findRec(recs, "increase-memory-request")
	if rec == nil {
		t.Fatal("expected an increase-memory-request recommendation")
	}
	if rec.MemoryRequest.Suggested != int64(float64(300*mib)*1.25) {
		t.Log("exp:", int64(float64(300*mib)*1.25))
		t.Log("got:", rec.MemoryRequest.Suggested)
		t.Fatal("suggested memory mismatch")
	}
	// Peak 300Mi is within 15% of the 400Mi limit? 0.85*400=340 > 300, so no
	// raise-memory-limit expected.
	if findRec(recs, "raise-memory-limit") != nil {
		t.Fatal("did not expect raise-memory-limit")
	}
}

func TestComputeRecommendationsSpecOnly(t *testing.T) {
	t.Parallel()

	// No usage data at all: only spec rules may fire.
	w := workload("Deployment", "prod", "worker", 2, packing.ContainerPacking{
		Name:   "main",
		CPU:    wres(100, 2000, 2, nil),       // 20x overcommit
		Memory: wres(64*mib, 512*mib, 2, nil), // 8x overcommit
	})

	recs := packing.ComputeRecommendations([]packing.WorkloadPacking{w})
	if findRec(recs, "tighten-cpu-overcommit") == nil {
		t.Fatal("expected tighten-cpu-overcommit")
	}
	if findRec(recs, "tighten-memory-overcommit") == nil {
		t.Fatal("expected tighten-memory-overcommit")
	}
	for _, r := range recs {
		if strings.HasPrefix(r.Type, "reduce-") || strings.HasPrefix(r.Type, "increase-") {
			t.Log("got:", r.Type)
			t.Fatal("usage-based rules must not fire without samples")
		}
	}
}

func TestComputeNodeShapes(t *testing.T) {
	t.Parallel()

	const gib = 1024 * mib
	// CPU-bound pool: cpu 80% requested, memory 20%.
	nodes := []packing.NodePacking{
		{
			Name:   "n1",
			Labels: map[string]string{"karpenter.sh/nodepool": "general", "node.kubernetes.io/instance-type": "m5.xlarge"},
			CPU:    packing.ResourceValues{Allocatable: 4000, Requested: 3200, RequestRatio: 0.8},
			Memory: packing.ResourceValues{Allocatable: 16 * gib, Requested: 3 * gib, RequestRatio: 0.1875},
		},
		{
			Name:   "n2",
			Labels: map[string]string{"karpenter.sh/nodepool": "general", "node.kubernetes.io/instance-type": "m5.xlarge"},
			CPU:    packing.ResourceValues{Allocatable: 4000, Requested: 3000, RequestRatio: 0.75},
			Memory: packing.ResourceValues{Allocatable: 16 * gib, Requested: 4 * gib, RequestRatio: 0.25},
		},
	}

	shapes := packing.ComputeNodeShapes(nodes)
	if len(shapes) != 1 {
		t.Log("exp:", 1)
		t.Log("got:", len(shapes))
		t.Fatal("pool count mismatch")
	}
	s := shapes[0]
	if s.Pool != "general" || s.NodeCount != 2 {
		t.Log("exp:", "general 2")
		t.Log("got:", s.Pool, s.NodeCount)
		t.Fatal("pool identity mismatch")
	}
	if s.Verdict != "cpu-bound" {
		t.Log("exp:", "cpu-bound")
		t.Log("got:", s.Verdict)
		t.Fatal("verdict mismatch")
	}
	if !strings.Contains(s.Suggestion, "Karpenter") {
		t.Log("got:", s.Suggestion)
		t.Fatal("expected a Karpenter hint for a karpenter pool")
	}
	if len(s.InstanceTypes) != 1 || s.InstanceTypes[0] != "m5.xlarge" {
		t.Log("exp:", "m5.xlarge")
		t.Log("got:", s.InstanceTypes)
		t.Fatal("instance types mismatch")
	}
}

func TestPoolOf(t *testing.T) {
	t.Parallel()

	if got := packing.PoolOf(map[string]string{"lke.linode.com/pool-id": "843671"}); got != "843671" {
		t.Log("exp:", "843671")
		t.Log("got:", got)
		t.Fatal("lke pool mismatch")
	}
	if got := packing.PoolOf(map[string]string{}); got != "(ungrouped)" {
		t.Log("exp:", "(ungrouped)")
		t.Log("got:", got)
		t.Fatal("ungrouped mismatch")
	}
}
