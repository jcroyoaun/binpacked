package packing_test

import (
	"testing"

	"github.com/zillow/binpacked/internal/packing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func makeNode(name string, cpuMillis, memBytes, maxPods int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"nodepool": "default"},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(memBytes, resource.BinarySI),
				corev1.ResourcePods:   *resource.NewQuantity(maxPods, resource.DecimalSI),
			},
		},
	}
}

func makePod(name, namespace, nodeName string, cpuReqMillis, memReqBytes int64) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Name: "main",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuReqMillis, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(memReqBytes, resource.BinarySI),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuReqMillis*2, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(memReqBytes*2, resource.BinarySI),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func makeBestEffortPod(name, namespace, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{{Name: "main"}},
		},
		Status: corev1.PodStatus{
			Phase:    corev1.PodRunning,
			QOSClass: corev1.PodQOSBestEffort,
		},
	}
}

func makeDaemonSetPod(name, namespace, nodeName string, cpuReqMillis, memReqBytes int64) *corev1.Pod {
	pod := makePod(name, namespace, nodeName, cpuReqMillis, memReqBytes)
	pod.OwnerReferences = []metav1.OwnerReference{
		{Kind: "DaemonSet", Name: "my-ds"},
	}
	return pod
}

func TestComputeForNode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		node  *corev1.Node
		pods  []*corev1.Pod
		check func(t *testing.T, np packing.NodePacking)
	}{
		{
			name: "single pod",
			node: makeNode("node-a", 4000, 8*1024*1024*1024, 110),
			pods: []*corev1.Pod{
				makePod("pod-1", "default", "node-a", 1000, 2*1024*1024*1024),
			},
			check: func(t *testing.T, np packing.NodePacking) {
				t.Helper()
				if np.CPU.Requested != 1000 {
					t.Log("exp:", 1000)
					t.Log("got:", np.CPU.Requested)
					t.Fatal("CPU requested mismatch")
				}
				if np.CPU.RequestRatio != 0.25 {
					t.Log("exp:", 0.25)
					t.Log("got:", np.CPU.RequestRatio)
					t.Fatal("CPU ratio mismatch")
				}
				if np.Pods.Count != 1 {
					t.Log("exp:", 1)
					t.Log("got:", np.Pods.Count)
					t.Fatal("pod count mismatch")
				}
				if np.CPU.Limits != 2000 {
					t.Log("exp:", 2000)
					t.Log("got:", np.CPU.Limits)
					t.Fatal("CPU limits mismatch")
				}
			},
		},
		{
			name: "no pods",
			node: makeNode("node-a", 4000, 8*1024*1024*1024, 110),
			pods: nil,
			check: func(t *testing.T, np packing.NodePacking) {
				t.Helper()
				if np.CPU.RequestRatio != 0 {
					t.Log("exp:", 0.0)
					t.Log("got:", np.CPU.RequestRatio)
					t.Fatal("empty node should have 0 ratio")
				}
				if np.Pods.Count != 0 {
					t.Log("exp:", 0)
					t.Log("got:", np.Pods.Count)
					t.Fatal("pod count should be 0")
				}
			},
		},
		{
			name: "besteffort pods flagged and contribute no requests",
			node: makeNode("node-a", 4000, 8*1024*1024*1024, 110),
			pods: []*corev1.Pod{
				makeBestEffortPod("be-pod", "default", "node-a"),
			},
			check: func(t *testing.T, np packing.NodePacking) {
				t.Helper()
				if np.BestEffortPodCount != 1 {
					t.Log("exp:", 1)
					t.Log("got:", np.BestEffortPodCount)
					t.Fatal("besteffort count mismatch")
				}
				if np.CPU.Requested != 0 {
					t.Log("exp:", 0)
					t.Log("got:", np.CPU.Requested)
					t.Fatal("besteffort pods should not contribute to requests")
				}
			},
		},
		{
			name: "daemonset pods flagged",
			node: makeNode("node-a", 4000, 8*1024*1024*1024, 110),
			pods: []*corev1.Pod{
				makeDaemonSetPod("ds-pod", "kube-system", "node-a", 100, 128*1024*1024),
			},
			check: func(t *testing.T, np packing.NodePacking) {
				t.Helper()
				if np.DaemonSetPodCount != 1 {
					t.Log("exp:", 1)
					t.Log("got:", np.DaemonSetPodCount)
					t.Fatal("daemonset count mismatch")
				}
				// DaemonSet pods still contribute to requests.
				if np.CPU.Requested != 100 {
					t.Log("exp:", 100)
					t.Log("got:", np.CPU.Requested)
					t.Fatal("daemonset pod CPU should be counted")
				}
			},
		},
		{
			name: "bottleneck is memory when memory ratio highest",
			node: makeNode("node-a", 4000, 4*1024*1024*1024, 110),
			pods: []*corev1.Pod{
				makePod("pod-1", "default", "node-a", 500, 3*1024*1024*1024),
			},
			check: func(t *testing.T, np packing.NodePacking) {
				t.Helper()
				if np.Bottleneck != "memory" {
					t.Log("exp:", "memory")
					t.Log("got:", np.Bottleneck)
					t.Fatal("bottleneck should be memory")
				}
			},
		},
		{
			name: "multiple pods aggregate",
			node: makeNode("node-a", 4000, 8*1024*1024*1024, 110),
			pods: []*corev1.Pod{
				makePod("pod-1", "default", "node-a", 1000, 1*1024*1024*1024),
				makePod("pod-2", "default", "node-a", 2000, 3*1024*1024*1024),
			},
			check: func(t *testing.T, np packing.NodePacking) {
				t.Helper()
				if np.CPU.Requested != 3000 {
					t.Log("exp:", 3000)
					t.Log("got:", np.CPU.Requested)
					t.Fatal("CPU requests should sum")
				}
				if np.Pods.Count != 2 {
					t.Log("exp:", 2)
					t.Log("got:", np.Pods.Count)
					t.Fatal("pod count mismatch")
				}
				if np.CPU.RequestRatio != 0.75 {
					t.Log("exp:", 0.75)
					t.Log("got:", np.CPU.RequestRatio)
					t.Fatal("CPU ratio mismatch")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			np := packing.ComputeForNode(test.node, test.pods)
			test.check(t, np)
		})
	}
}

func TestComputeClusterSummary(t *testing.T) {
	t.Parallel()

	nodes := []packing.NodePacking{
		{
			Name:       "node-a",
			CPU:        packing.ResourceValues{Allocatable: 4000, Requested: 3600, RequestRatio: 0.90},
			Memory:     packing.ResourceValues{Allocatable: 8 * 1024 * 1024 * 1024, Requested: 2 * 1024 * 1024 * 1024, RequestRatio: 0.25},
			Pods:       packing.PodValues{Allocatable: 110, Count: 50, Ratio: 0.45},
			Bottleneck: "cpu",
		},
		{
			Name:       "node-b",
			CPU:        packing.ResourceValues{Allocatable: 4000, Requested: 1000, RequestRatio: 0.25},
			Memory:     packing.ResourceValues{Allocatable: 8 * 1024 * 1024 * 1024, Requested: 6 * 1024 * 1024 * 1024, RequestRatio: 0.75},
			Pods:       packing.PodValues{Allocatable: 110, Count: 80, Ratio: 0.73},
			Bottleneck: "memory",
		},
	}

	s := packing.ComputeClusterSummary(nodes)

	if s.TotalNodes != 2 {
		t.Log("exp:", 2)
		t.Log("got:", s.TotalNodes)
		t.Fatal("total nodes mismatch")
	}
	if s.TotalPods != 130 {
		t.Log("exp:", 130)
		t.Log("got:", s.TotalPods)
		t.Fatal("total pods mismatch")
	}
	if s.CPU.Allocatable != 8000 {
		t.Log("exp:", 8000)
		t.Log("got:", s.CPU.Allocatable)
		t.Fatal("CPU allocatable mismatch")
	}

	// node-a: cpu bottleneck -> stranded memory = 8GiB - 2GiB = 6GiB
	// node-b: memory bottleneck -> stranded CPU = 4000 - 1000 = 3000m
	if s.StrandedResources.CPUMillicores != 3000 {
		t.Log("exp:", 3000)
		t.Log("got:", s.StrandedResources.CPUMillicores)
		t.Fatal("stranded CPU mismatch")
	}
	expectedStrandedMem := int64(6 * 1024 * 1024 * 1024)
	if s.StrandedResources.MemoryBytes != expectedStrandedMem {
		t.Log("exp:", expectedStrandedMem)
		t.Log("got:", s.StrandedResources.MemoryBytes)
		t.Fatal("stranded memory mismatch")
	}

	// Distribution: node-a dominant=0.90 -> "90-100", node-b dominant=0.75 -> "75-90"
	if s.Distribution["90-100"] != 1 {
		t.Log("exp:", 1)
		t.Log("got:", s.Distribution["90-100"])
		t.Fatal("distribution 90-100 mismatch")
	}
	if s.Distribution["75-90"] != 1 {
		t.Log("exp:", 1)
		t.Log("got:", s.Distribution["75-90"])
		t.Fatal("distribution 75-90 mismatch")
	}

	// Least packed first (lowest dominant), most packed first (highest dominant).
	if s.LeastPacked[0].Name != "node-b" {
		t.Log("exp:", "node-b")
		t.Log("got:", s.LeastPacked[0].Name)
		t.Fatal("least packed mismatch")
	}
	if s.MostPacked[0].Name != "node-a" {
		t.Log("exp:", "node-a")
		t.Log("got:", s.MostPacked[0].Name)
		t.Fatal("most packed mismatch")
	}
}

func TestPodToInfo(t *testing.T) {
	t.Parallel()

	pod := makePod("my-pod", "production", "node-a", 500, 1*1024*1024*1024)
	pod.Status.Phase = corev1.PodRunning

	info := packing.PodToInfo(pod)

	if info.Name != "my-pod" {
		t.Log("exp:", "my-pod")
		t.Log("got:", info.Name)
		t.Fatal("name mismatch")
	}
	if info.Namespace != "production" {
		t.Log("exp:", "production")
		t.Log("got:", info.Namespace)
		t.Fatal("namespace mismatch")
	}
	if info.CPU.Requested != 500 {
		t.Log("exp:", 500)
		t.Log("got:", info.CPU.Requested)
		t.Fatal("CPU requested mismatch")
	}
	if info.Phase != "Running" {
		t.Log("exp:", "Running")
		t.Log("got:", info.Phase)
		t.Fatal("phase mismatch")
	}
}
