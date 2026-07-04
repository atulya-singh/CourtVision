package executor

import (
	"context"
	"testing"

	"github.com/atulya-singh/CourtVision/internal/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDistribute(t *testing.T) {
	tests := []struct {
		name    string
		current []int64
		total   int64
		want    []int64
	}{
		{
			name:    "proportional split preserves shares and sums to total",
			current: []int64{800, 200}, // 80/20
			total:   1300,
			want:    []int64{1040, 260},
		},
		{
			name:    "single bucket gets the whole total",
			current: []int64{500},
			total:   1300,
			want:    []int64{1300},
		},
		{
			name:    "all-zero falls back to even split",
			current: []int64{0, 0, 0},
			total:   900,
			want:    []int64{300, 300, 300},
		},
		{
			name:    "even split remainder goes to last bucket",
			current: []int64{0, 0, 0},
			total:   1000,
			want:    []int64{333, 333, 334},
		},
		{
			name:    "scale down works proportionally",
			current: []int64{800, 200},
			total:   500,
			want:    []int64{400, 100},
		},
		{
			name:    "rounding remainder lands on last bucket",
			current: []int64{1, 1, 1},
			total:   10,
			want:    []int64{3, 3, 4},
		},
		{
			name:    "empty input returns empty",
			current: []int64{},
			total:   100,
			want:    []int64{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := distribute(tt.current, tt.total)
			if len(got) != len(tt.want) {
				t.Fatalf("length mismatch: got %v, want %v", got, tt.want)
			}
			var sum int64
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %d, want %d (full: %v)", i, got[i], tt.want[i], got)
				}
				sum += got[i]
			}
			// The core invariant: shares always sum exactly to total.
			if len(got) > 0 && sum != tt.total {
				t.Errorf("shares sum to %d, want total %d", sum, tt.total)
			}
		})
	}
}

// multiContainerObjects builds the Pod -> ReplicaSet -> Deployment ownership
// chain that owningDeployment walks, with a two-container pod template (a big
// "app" and a small "sidecar") so we can assert proportional distribution.
func multiContainerObjects(appCPU, sidecarCPU, appMem, sidecarMem string) (*appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod) {
	limits := func(cpu, mem string) corev1.ResourceList {
		rl := corev1.ResourceList{}
		if cpu != "" {
			rl[corev1.ResourceCPU] = resource.MustParse(cpu)
		}
		if mem != "" {
			rl[corev1.ResourceMemory] = resource.MustParse(mem)
		}
		return rl
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Resources: corev1.ResourceRequirements{Limits: limits(appCPU, appMem)}},
						{Name: "sidecar", Resources: corev1.ResourceRequirements{Limits: limits(sidecarCPU, sidecarMem)}},
					},
				},
			},
		},
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "web-abc123",
			Namespace:       "default",
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web"}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "web-abc123-xyz",
			Namespace:       "default",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-abc123"}},
		},
	}
	return dep, rs, pod
}

func TestPatchLimits_MultiContainer(t *testing.T) {
	// app=800m/800Mi, sidecar=200m/200Mi → pod totals 1000m / 1000Mi.
	dep, rs, pod := multiContainerObjects("800m", "200m", "800Mi", "200Mi")
	client := fake.NewSimpleClientset(dep, rs, pod)
	e := &K8sExecutor{client: client}

	d := &types.Decision{
		Action:      types.ActionPatchLimits,
		Namespace:   "default",
		TargetPod:   "web-abc123-xyz",
		NewCPULimit: 1300, // millicores, pod-level target
		NewMemLimit: 1300, // MB, pod-level target
	}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	got, err := client.AppsV1().Deployments("default").Get(context.Background(), "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	containers := got.Spec.Template.Spec.Containers

	// CPU: 80/20 of 1300 → 1040 / 260, summing to 1300.
	appCPU := containers[0].Resources.Limits.Cpu().MilliValue()
	sideCPU := containers[1].Resources.Limits.Cpu().MilliValue()
	if appCPU != 1040 || sideCPU != 260 {
		t.Errorf("CPU shares: app=%dm sidecar=%dm, want 1040m/260m", appCPU, sideCPU)
	}
	if appCPU+sideCPU != 1300 {
		t.Errorf("CPU shares sum to %dm, want 1300m", appCPU+sideCPU)
	}

	// Memory: same 80/20 of 1300Mi → 1040Mi / 260Mi.
	appMemMB := containers[0].Resources.Limits.Memory().Value() / (1024 * 1024)
	sideMemMB := containers[1].Resources.Limits.Memory().Value() / (1024 * 1024)
	if appMemMB != 1040 || sideMemMB != 260 {
		t.Errorf("Mem shares: app=%dMB sidecar=%dMB, want 1040MB/260MB", appMemMB, sideMemMB)
	}
	if appMemMB+sideMemMB != 1300 {
		t.Errorf("Mem shares sum to %dMB, want 1300MB", appMemMB+sideMemMB)
	}
}

func TestPatchLimits_CPUOnly(t *testing.T) {
	dep, rs, pod := multiContainerObjects("800m", "200m", "800Mi", "200Mi")
	client := fake.NewSimpleClientset(dep, rs, pod)
	e := &K8sExecutor{client: client}

	d := &types.Decision{
		Action:      types.ActionPatchLimits,
		Namespace:   "default",
		TargetPod:   "web-abc123-xyz",
		NewCPULimit: 1300, // only CPU set
	}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	got, err := client.AppsV1().Deployments("default").Get(context.Background(), "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	containers := got.Spec.Template.Spec.Containers

	// CPU changed...
	if containers[0].Resources.Limits.Cpu().MilliValue() != 1040 {
		t.Errorf("app CPU = %dm, want 1040m", containers[0].Resources.Limits.Cpu().MilliValue())
	}
	// ...but memory limits are left exactly as they were.
	if mem := containers[0].Resources.Limits.Memory().Value() / (1024 * 1024); mem != 800 {
		t.Errorf("app Mem = %dMB, want untouched 800MB", mem)
	}
	if mem := containers[1].Resources.Limits.Memory().Value() / (1024 * 1024); mem != 200 {
		t.Errorf("sidecar Mem = %dMB, want untouched 200MB", mem)
	}
}
