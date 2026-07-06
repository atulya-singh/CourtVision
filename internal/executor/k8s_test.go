package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/atulya-singh/CourtVision/internal/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
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

// conflictOnce returns a reactor that fails the first matching call with a real
// 409 Conflict (so RetryOnConflict retries it) and then steps aside on every
// later call, letting the fake's default tracker apply the write. The returned
// pointer lets a test assert how many times the reactor fired.
func conflictOnce(resource string) (func(clienttesting.Action) (bool, runtime.Object, error), *int) {
	calls := 0
	fn := func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Resource: resource}, "obj",
				errors.New("the object has been modified; please apply your changes to the latest version"),
			)
		}
		return false, nil, nil // fall through to the default tracker
	}
	return fn, &calls
}

func TestPatchLimits_RetriesOnConflict(t *testing.T) {
	dep, rs, pod := multiContainerObjects("800m", "200m", "800Mi", "200Mi")
	client := fake.NewSimpleClientset(dep, rs, pod)
	react, calls := conflictOnce("deployments")
	client.PrependReactor("update", "deployments", react)
	e := &K8sExecutor{client: client}

	d := &types.Decision{
		Action:      types.ActionPatchLimits,
		Namespace:   "default",
		TargetPod:   "web-abc123-xyz",
		NewCPULimit: 1300,
	}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("Execute should succeed after a retry, got: %v", err)
	}
	if *calls < 2 {
		t.Errorf("expected the update to be retried after a conflict, reactor fired %d time(s)", *calls)
	}

	// The retried attempt must still apply the patch to the latest object.
	got, _ := client.AppsV1().Deployments("default").Get(context.Background(), "web", metav1.GetOptions{})
	if cpu := got.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().MilliValue(); cpu != 1040 {
		t.Errorf("app CPU after retry = %dm, want 1040m", cpu)
	}
}

func TestScaleDown_RetriesOnConflict(t *testing.T) {
	dep, rs, pod := multiContainerObjects("800m", "200m", "800Mi", "200Mi")
	three := int32(3)
	dep.Spec.Replicas = &three
	client := fake.NewSimpleClientset(dep, rs, pod)
	react, calls := conflictOnce("deployments")
	client.PrependReactor("update", "deployments", react)
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionScaleDown, Namespace: "default", TargetPod: "web-abc123-xyz"}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("Execute should succeed after a retry, got: %v", err)
	}
	if *calls < 2 {
		t.Errorf("expected a retry after conflict, reactor fired %d time(s)", *calls)
	}

	got, _ := client.AppsV1().Deployments("default").Get(context.Background(), "web", metav1.GetOptions{})
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 2 {
		t.Errorf("replicas after retry = %v, want 2", got.Spec.Replicas)
	}
}

func TestCordonNode_RetriesOnConflict(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	client := fake.NewSimpleClientset(node)
	react, calls := conflictOnce("nodes")
	client.PrependReactor("update", "nodes", react)
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionCordonNode, TargetNode: "node-1"}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("Execute should succeed after a retry, got: %v", err)
	}
	if *calls < 2 {
		t.Errorf("expected a retry after conflict, reactor fired %d time(s)", *calls)
	}

	got, _ := client.CoreV1().Nodes().Get(context.Background(), "node-1", metav1.GetOptions{})
	if !got.Spec.Unschedulable {
		t.Error("node should be cordoned after the retried update")
	}
}

func TestPatchLimits_NonConflictErrorNotRetried(t *testing.T) {
	dep, rs, pod := multiContainerObjects("800m", "200m", "800Mi", "200Mi")
	client := fake.NewSimpleClientset(dep, rs, pod)

	// A plain (non-conflict) error must surface immediately and must NOT be
	// retried — otherwise a genuinely broken update would be hammered N times.
	calls := 0
	client.PrependReactor("update", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		return true, nil, errors.New("boom")
	})
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionPatchLimits, Namespace: "default", TargetPod: "web-abc123-xyz", NewCPULimit: 1300}
	err := e.Execute(context.Background(), d)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("want the inner error returned unchanged, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("non-conflict error must not be retried, reactor fired %d time(s)", calls)
	}
}

func TestEvict_UsesEvictionAPINotDelete(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-xyz", Namespace: "default"}}
	client := fake.NewSimpleClientset(pod)
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionEvictAndMove, Namespace: "default", TargetPod: "web-xyz"}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// The core guarantee: we submit an Eviction (which honors PDBs), never a raw
	// pod Delete (which bypasses them).
	var sawEviction, sawDelete bool
	for _, a := range client.Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource == "pods" && a.GetSubresource() == "eviction" {
			sawEviction = true
		}
		if a.GetVerb() == "delete" && a.GetResource().Resource == "pods" {
			sawDelete = true
		}
	}
	if !sawEviction {
		t.Error("expected an eviction subresource create, saw none")
	}
	if sawDelete {
		t.Error("evict must not issue a raw pod delete (that bypasses PodDisruptionBudgets)")
	}
}

func TestEvict_PDBBlockedReturnsError(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-xyz", Namespace: "default"}}
	client := fake.NewSimpleClientset(pod)
	// A PDB blocking the eviction surfaces as 429 TooManyRequests.
	client.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget", 10)
	})
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionEvictAndMove, Namespace: "default", TargetPod: "web-xyz"}
	err := e.Execute(context.Background(), d)
	if err == nil {
		t.Fatal("a PDB-blocked eviction must return an error, got nil")
	}
	// It must remain identifiable as a 429 (wrapped, not swallowed) and name the pod.
	if !apierrors.IsTooManyRequests(err) {
		t.Errorf("blocked eviction should stay a TooManyRequests error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "web-xyz") {
		t.Errorf("error should name the target pod, got: %v", err)
	}
}

func TestEvict_AlreadyGoneIsSuccess(t *testing.T) {
	client := fake.NewSimpleClientset() // no pod present
	client.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "web-xyz")
	})
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionEvictAndMove, Namespace: "default", TargetPod: "web-xyz"}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Errorf("evicting an already-gone pod should be a no-op success, got: %v", err)
	}
}

func boolPtr(b bool) *bool    { return &b }
func int32Ptr(i int32) *int32 { return &i }

// podOnNode builds a running pod already scheduled onto nodeName.
func podOnNode(name, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: nodeName},
	}
}

// node builds a node that is Ready by default; set schedulable=false to cordon it
// or ready=false to mark it NotReady.
func node(name string, schedulable, ready bool) *corev1.Node {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{Unschedulable: !schedulable},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}},
		},
	}
}

// evictionIssued reports whether any recorded action was an Eviction subresource
// create — i.e. we actually tried to evict rather than skipping/failing pre-flight.
func evictionIssued(client *fake.Clientset) bool {
	for _, a := range client.Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource == "pods" && a.GetSubresource() == "eviction" {
			return true
		}
	}
	return false
}

func TestEvict_MoveToValidNode(t *testing.T) {
	client := fake.NewSimpleClientset(podOnNode("web-xyz", "node-a"), node("node-b", true, true))
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionEvictAndMove, Namespace: "default", TargetPod: "web-xyz", TargetNode: "node-b"}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("moving to a Ready, schedulable node should succeed, got: %v", err)
	}
	if !evictionIssued(client) {
		t.Error("a valid move should still issue an eviction")
	}
}

func TestEvict_MoveTargetNodeMissing(t *testing.T) {
	client := fake.NewSimpleClientset(podOnNode("web-xyz", "node-a")) // node-b absent
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionEvictAndMove, Namespace: "default", TargetPod: "web-xyz", TargetNode: "node-b"}
	err := e.Execute(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("moving to a missing node should fail clearly, got: %v", err)
	}
	if evictionIssued(client) {
		t.Error("must not evict when the destination node is missing")
	}
}

func TestEvict_MoveTargetNodeCordoned(t *testing.T) {
	client := fake.NewSimpleClientset(podOnNode("web-xyz", "node-a"), node("node-b", false, true))
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionEvictAndMove, Namespace: "default", TargetPod: "web-xyz", TargetNode: "node-b"}
	err := e.Execute(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "cordoned") {
		t.Fatalf("moving to a cordoned node should fail clearly, got: %v", err)
	}
	if evictionIssued(client) {
		t.Error("must not evict toward a cordoned node")
	}
}

func TestEvict_MoveTargetNodeNotReady(t *testing.T) {
	client := fake.NewSimpleClientset(podOnNode("web-xyz", "node-a"), node("node-b", true, false))
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionEvictAndMove, Namespace: "default", TargetPod: "web-xyz", TargetNode: "node-b"}
	err := e.Execute(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "not Ready") {
		t.Fatalf("moving to a NotReady node should fail clearly, got: %v", err)
	}
	if evictionIssued(client) {
		t.Error("must not evict toward a NotReady node")
	}
}

func TestEvict_AlreadyOnTargetNodeIsNoOp(t *testing.T) {
	client := fake.NewSimpleClientset(podOnNode("web-xyz", "node-b"), node("node-b", true, true))
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionEvictAndMove, Namespace: "default", TargetPod: "web-xyz", TargetNode: "node-b"}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("a pod already on the target node should be a no-op success, got: %v", err)
	}
	if evictionIssued(client) {
		t.Error("must not evict a pod that is already on the target node")
	}
}

// podOwnedBy builds a pod directly controlled by a workload of the given kind, as
// StatefulSets and DaemonSets own their pods (no intervening ReplicaSet).
func podOwnedBy(kind, ownerName, podName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            podName,
			Namespace:       "default",
			OwnerReferences: []metav1.OwnerReference{{Kind: kind, Name: ownerName, Controller: boolPtr(true)}},
		},
	}
}

func cpuLimits(cpu string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)}}
}

func TestPatchLimits_StatefulSet(t *testing.T) {
	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "db", Resources: cpuLimits("500m")}}},
			},
		},
	}
	client := fake.NewSimpleClientset(ss, podOwnedBy("StatefulSet", "db", "db-0"))
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionPatchLimits, Namespace: "default", TargetPod: "db-0", NewCPULimit: 1000}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	got, _ := client.AppsV1().StatefulSets("default").Get(context.Background(), "db", metav1.GetOptions{})
	if cpu := got.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().MilliValue(); cpu != 1000 {
		t.Errorf("statefulset CPU limit = %dm, want 1000m", cpu)
	}
}

func TestPatchLimits_DaemonSet(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default"},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Resources: cpuLimits("100m")}}},
			},
		},
	}
	client := fake.NewSimpleClientset(ds, podOwnedBy("DaemonSet", "agent", "agent-abc"))
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionPatchLimits, Namespace: "default", TargetPod: "agent-abc", NewCPULimit: 250}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	got, _ := client.AppsV1().DaemonSets("default").Get(context.Background(), "agent", metav1.GetOptions{})
	if cpu := got.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().MilliValue(); cpu != 250 {
		t.Errorf("daemonset CPU limit = %dm, want 250m", cpu)
	}
}

func TestScaleDown_StatefulSet(t *testing.T) {
	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: int32Ptr(3)},
	}
	client := fake.NewSimpleClientset(ss, podOwnedBy("StatefulSet", "db", "db-0"))
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionScaleDown, Namespace: "default", TargetPod: "db-0"}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	got, _ := client.AppsV1().StatefulSets("default").Get(context.Background(), "db", metav1.GetOptions{})
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 2 {
		t.Errorf("statefulset replicas = %v, want 2", got.Spec.Replicas)
	}
}

func TestScaleDown_DaemonSetRejected(t *testing.T) {
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default"}}
	client := fake.NewSimpleClientset(ds, podOwnedBy("DaemonSet", "agent", "agent-abc"))
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionScaleDown, Namespace: "default", TargetPod: "agent-abc"}
	err := e.Execute(context.Background(), d)
	if err == nil {
		t.Fatal("scaling a DaemonSet must be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "daemonset") {
		t.Errorf("error should explain a DaemonSet is not scalable, got: %v", err)
	}
}

func TestPatchLimits_UnsupportedCRDWorkload(t *testing.T) {
	// A ReplicaSet owned by a CRD controller (e.g. an Argo Rollout), not a
	// Deployment. We can't mutate the CRD, but the failure must be clear.
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "rollout-abc123",
			Namespace:       "default",
			OwnerReferences: []metav1.OwnerReference{{Kind: "Rollout", Name: "canary", Controller: boolPtr(true)}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "rollout-abc123-xyz",
			Namespace:       "default",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "rollout-abc123", Controller: boolPtr(true)}},
		},
	}
	client := fake.NewSimpleClientset(rs, pod)
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionPatchLimits, Namespace: "default", TargetPod: "rollout-abc123-xyz", NewCPULimit: 500}
	err := e.Execute(context.Background(), d)
	if err == nil {
		t.Fatal("patching a CRD-owned workload must fail clearly, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported workload kind") || !strings.Contains(err.Error(), "Rollout") {
		t.Errorf("error should name the unsupported kind, got: %v", err)
	}
}

func TestScaleDown_BarePodRejected(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "lonely", Namespace: "default"}} // no owner refs
	client := fake.NewSimpleClientset(pod)
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionScaleDown, Namespace: "default", TargetPod: "lonely"}
	err := e.Execute(context.Background(), d)
	if err == nil {
		t.Fatal("scaling a bare pod must fail, got nil")
	}
	if !strings.Contains(err.Error(), "bare pod") {
		t.Errorf("error should explain there is no controlling workload, got: %v", err)
	}
}

func TestScaleDown_BareReplicaSet(t *testing.T) {
	// A ReplicaSet with no higher controller is itself the scalable workload.
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "standalone", Namespace: "default"},
		Spec:       appsv1.ReplicaSetSpec{Replicas: int32Ptr(4)},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "standalone-xyz",
			Namespace:       "default",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "standalone", Controller: boolPtr(true)}},
		},
	}
	client := fake.NewSimpleClientset(rs, pod)
	e := &K8sExecutor{client: client}

	d := &types.Decision{Action: types.ActionScaleDown, Namespace: "default", TargetPod: "standalone-xyz"}
	if err := e.Execute(context.Background(), d); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	got, _ := client.AppsV1().ReplicaSets("default").Get(context.Background(), "standalone", metav1.GetOptions{})
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 3 {
		t.Errorf("bare replicaset replicas = %v, want 3", got.Spec.Replicas)
	}
}
