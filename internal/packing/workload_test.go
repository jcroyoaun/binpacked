package packing_test

import (
	"testing"

	"github.com/zillow/binpacked/internal/packing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type v1Pod = corev1.Pod

func ctrl() *bool {
	b := true
	return &b
}

func makeDeploymentPod(name, namespace, deployment, hash string) *v1Pod {
	pod := makePod(name, namespace, "node-a", 100, 128*1024*1024)
	pod.Labels = map[string]string{"pod-template-hash": hash}
	pod.OwnerReferences = []metav1.OwnerReference{
		{Kind: "ReplicaSet", Name: deployment + "-" + hash, Controller: ctrl()},
	}
	return pod
}

func TestResolveWorkload(t *testing.T) {
	t.Parallel()

	deployPod := makeDeploymentPod("web-6d4b75cb6d-abcde", "prod", "web", "6d4b75cb6d")
	kind, name := packing.ResolveWorkload(deployPod)
	if kind != "Deployment" || name != "web" {
		t.Log("exp:", "Deployment", "web")
		t.Log("got:", kind, name)
		t.Fatal("deployment resolution mismatch")
	}

	// Deployment names containing hyphens must survive hash stripping.
	hyphenPod := makeDeploymentPod("my-api-server-abc123def4-xyz", "prod", "my-api-server", "abc123def4")
	kind, name = packing.ResolveWorkload(hyphenPod)
	if kind != "Deployment" || name != "my-api-server" {
		t.Log("exp:", "Deployment", "my-api-server")
		t.Log("got:", kind, name)
		t.Fatal("hyphenated deployment resolution mismatch")
	}

	// ReplicaSet without the pod-template-hash label stays a ReplicaSet.
	rsPod := makePod("bare-rs-pod", "prod", "node-a", 100, 1024)
	rsPod.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "bare-rs", Controller: ctrl()}}
	kind, name = packing.ResolveWorkload(rsPod)
	if kind != "ReplicaSet" || name != "bare-rs" {
		t.Log("exp:", "ReplicaSet", "bare-rs")
		t.Log("got:", kind, name)
		t.Fatal("bare replicaset resolution mismatch")
	}

	dsPod := makeDaemonSetPod("ds-pod", "kube-system", "node-a", 50, 1024)
	kind, name = packing.ResolveWorkload(dsPod)
	if kind != "DaemonSet" || name != "my-ds" {
		t.Log("exp:", "DaemonSet", "my-ds")
		t.Log("got:", kind, name)
		t.Fatal("daemonset resolution mismatch")
	}

	// CronJob heuristic: job named <cronjob>-<unix-minutes>.
	cjPod := makePod("cleanup-29184840-x2v", "batch", "node-a", 100, 1024)
	cjPod.OwnerReferences = []metav1.OwnerReference{{Kind: "Job", Name: "cleanup-29184840", Controller: ctrl()}}
	kind, name = packing.ResolveWorkload(cjPod)
	if kind != "CronJob" || name != "cleanup" {
		t.Log("exp:", "CronJob", "cleanup")
		t.Log("got:", kind, name)
		t.Fatal("cronjob resolution mismatch")
	}

	// Plain job keeps its own identity.
	jobPod := makePod("migrate-x2v", "batch", "node-a", 100, 1024)
	jobPod.OwnerReferences = []metav1.OwnerReference{{Kind: "Job", Name: "migrate", Controller: ctrl()}}
	kind, name = packing.ResolveWorkload(jobPod)
	if kind != "Job" || name != "migrate" {
		t.Log("exp:", "Job", "migrate")
		t.Log("got:", kind, name)
		t.Fatal("job resolution mismatch")
	}

	// No owner references: a bare pod.
	barePod := makePod("standalone", "default", "node-a", 100, 1024)
	kind, name = packing.ResolveWorkload(barePod)
	if kind != "Pod" || name != "standalone" {
		t.Log("exp:", "Pod", "standalone")
		t.Log("got:", kind, name)
		t.Fatal("bare pod resolution mismatch")
	}
}

func TestAggregateWorkloads(t *testing.T) {
	t.Parallel()

	pods := []*v1Pod{
		makeDeploymentPod("web-6d4b75cb6d-aaaaa", "prod", "web", "6d4b75cb6d"),
		makeDeploymentPod("web-6d4b75cb6d-bbbbb", "prod", "web", "6d4b75cb6d"),
		makeDeploymentPod("web-6d4b75cb6d-ccccc", "prod", "web", "6d4b75cb6d"),
		makePod("standalone", "default", "node-b", 250, 256*1024*1024),
	}
	pods[1].Spec.NodeName = "node-b"

	workloads := packing.AggregateWorkloads(pods, nil)
	if len(workloads) != 2 {
		t.Log("exp:", 2)
		t.Log("got:", len(workloads))
		t.Fatal("workload count mismatch")
	}

	// Sorted by namespace: default/Pod/standalone first, then prod/Deployment/web.
	web := workloads[1]
	if web.Kind != "Deployment" || web.Name != "web" || web.Namespace != "prod" {
		t.Log("exp:", "prod Deployment web")
		t.Log("got:", web.Namespace, web.Kind, web.Name)
		t.Fatal("workload identity mismatch")
	}
	if web.PodCount != 3 {
		t.Log("exp:", 3)
		t.Log("got:", web.PodCount)
		t.Fatal("pod count mismatch")
	}
	if web.CPU.RequestPerPod != 100 || web.CPU.RequestTotal != 300 {
		t.Log("exp:", 100, 300)
		t.Log("got:", web.CPU.RequestPerPod, web.CPU.RequestTotal)
		t.Fatal("cpu aggregation mismatch")
	}
	if len(web.Nodes) != 2 {
		t.Log("exp:", 2)
		t.Log("got:", len(web.Nodes), web.Nodes)
		t.Fatal("node spread mismatch")
	}
	if len(web.Containers) != 1 || web.Containers[0].Name != "main" {
		t.Log("exp:", "1 container named main")
		t.Log("got:", web.Containers)
		t.Fatal("container aggregation mismatch")
	}

	// Succeeded/Failed pods are excluded.
	donePod := makePod("done", "default", "node-a", 100, 1024)
	donePod.Status.Phase = "Succeeded"
	workloads = packing.AggregateWorkloads([]*v1Pod{donePod}, nil)
	if len(workloads) != 0 {
		t.Log("exp:", 0)
		t.Log("got:", len(workloads))
		t.Fatal("completed pods must be excluded")
	}
}

type stubUsage struct {
	cpu packing.UsageStats
	mem packing.UsageStats
}

func (s stubUsage) ContainerUsage(namespace, pod, container string) (packing.UsageStats, packing.UsageStats, bool) {
	return s.cpu, s.mem, true
}

func (s stubUsage) Window() int64 { return 3600 }

func TestAggregateWorkloadsWithUsage(t *testing.T) {
	t.Parallel()

	usage := stubUsage{
		cpu: packing.UsageStats{AvgPerPod: 20, MaxPerPod: 45, P95PerPod: 40, Samples: 60, WindowSeconds: 3600},
		mem: packing.UsageStats{AvgPerPod: 50 * 1024 * 1024, MaxPerPod: 60 * 1024 * 1024, P95PerPod: 58 * 1024 * 1024, Samples: 60, WindowSeconds: 3600},
	}
	pods := []*v1Pod{
		makeDeploymentPod("web-6d4b75cb6d-aaaaa", "prod", "web", "6d4b75cb6d"),
		makeDeploymentPod("web-6d4b75cb6d-bbbbb", "prod", "web", "6d4b75cb6d"),
	}

	workloads := packing.AggregateWorkloads(pods, usage)
	web := workloads[0]
	cpuUse := web.Containers[0].CPU.Usage
	if cpuUse == nil {
		t.Fatal("expected cpu usage stats")
	}
	if cpuUse.MaxPerPod != 45 || cpuUse.Samples != 120 || cpuUse.WindowSeconds != 3600 {
		t.Log("exp:", 45, 120, 3600)
		t.Log("got:", cpuUse.MaxPerPod, cpuUse.Samples, cpuUse.WindowSeconds)
		t.Fatal("merged usage stats mismatch")
	}
}
