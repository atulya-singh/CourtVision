package executor

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/atulya-singh/CourtVision/internal/types"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/retry"
)

// K8sExecutor performs real mutations against a live Kubernetes cluster. Every
// method here changes real state, so this executor is only ever selected when
// the operator runs in LIVE mode (not --dry-run) against a real --metrics k8s
// source. Run it against a throwaway cluster (e.g. kind) before trusting it on
// anything you care about.
type K8sExecutor struct {
	client kubernetes.Interface
}

// NewK8sExecutor builds a client from the local kubeconfig, the same way the
// metrics provider does. contextName selects which kubeconfig context to target
// so each ClusterWorker can mutate its own cluster; passing "" falls back to the
// kubeconfig current-context. It does not talk to the cluster yet; failures here
// are only about loading credentials.
func NewK8sExecutor(contextName string) (*K8sExecutor, error) {
	home := homedir.HomeDir()
	if home == "" {
		return nil, fmt.Errorf("could not find home directory")
	}

	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: filepath.Join(home, ".kube", "config")}
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	return &K8sExecutor{client: client}, nil
}

func (e *K8sExecutor) Execute(ctx context.Context, d *types.Decision) error {
	switch d.Action {
	case types.ActionNone:
		return nil
	case types.ActionPatchLimits:
		return e.patchLimits(ctx, d)
	case types.ActionScaleDown:
		return e.scaleDown(ctx, d)
	case types.ActionCordonNode:
		return e.cordonNode(ctx, d)
	case types.ActionEvictAndMove:
		return e.evict(ctx, d)
	default:
		return fmt.Errorf("unknown action %q", d.Action)
	}
}

// patchLimits raises the resource limits on the workload behind the target pod.
// A running pod's limits cannot be edited in place, so we patch the owning
// workload's pod template instead; Kubernetes then rolls out new pods with the
// new limits. The workload may be a Deployment, StatefulSet, DaemonSet, or bare
// ReplicaSet — all of which carry a Spec.Template.Spec.Containers.
//
// The decision carries a single pod-level target (NewCPULimit/NewMemLimit),
// because the metrics that produced it were summed across every container in the
// pod. We therefore distribute that target across all containers in proportion to
// their current limits, rather than dumping the whole pod budget onto the first
// container. Each resource is handled independently, and only when the decision
// actually sets it (see setContainerLimits).
//
// The workload kind is resolved once (ownership is stable), but the read-modify-
// write runs under RetryOnConflict: on a live cluster other controllers touch the
// object constantly, so an Update carrying a stale resourceVersion returns a 409
// Conflict. Each attempt refetches a fresh object, so distribute recomputes
// against the current limits; any non-conflict error is returned immediately.
func (e *K8sExecutor) patchLimits(ctx context.Context, d *types.Decision) error {
	w, err := e.owningWorkload(ctx, d.Namespace, d.TargetPod)
	if err != nil {
		return err
	}
	apps := e.client.AppsV1()
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		switch w.Kind {
		case "Deployment":
			obj, err := apps.Deployments(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if err := setContainerLimits(obj.Spec.Template.Spec.Containers, d, w); err != nil {
				return err
			}
			_, err = apps.Deployments(w.Namespace).Update(ctx, obj, metav1.UpdateOptions{})
			return err
		case "StatefulSet":
			obj, err := apps.StatefulSets(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if err := setContainerLimits(obj.Spec.Template.Spec.Containers, d, w); err != nil {
				return err
			}
			_, err = apps.StatefulSets(w.Namespace).Update(ctx, obj, metav1.UpdateOptions{})
			return err
		case "DaemonSet":
			obj, err := apps.DaemonSets(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if err := setContainerLimits(obj.Spec.Template.Spec.Containers, d, w); err != nil {
				return err
			}
			_, err = apps.DaemonSets(w.Namespace).Update(ctx, obj, metav1.UpdateOptions{})
			return err
		case "ReplicaSet":
			obj, err := apps.ReplicaSets(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if err := setContainerLimits(obj.Spec.Template.Spec.Containers, d, w); err != nil {
				return err
			}
			_, err = apps.ReplicaSets(w.Namespace).Update(ctx, obj, metav1.UpdateOptions{})
			return err
		default:
			return unsupportedWorkload("patch_limits", w, d.TargetPod)
		}
	})
}

// setContainerLimits distributes the decision's pod-level CPU/memory targets
// across the given containers in proportion to their current limits (see
// distribute), mutating them in place. CPU and memory are each applied only when
// the decision sets that target. The workloadRef is only used for a clear error
// message when the template has no containers.
func setContainerLimits(containers []corev1.Container, d *types.Decision, w workloadRef) error {
	if len(containers) == 0 {
		return fmt.Errorf("%s %s/%s has no containers to patch", w.Kind, w.Namespace, w.Name)
	}

	if d.NewCPULimit > 0 {
		current := make([]int64, len(containers))
		for i := range containers {
			current[i] = containers[i].Resources.Limits.Cpu().MilliValue()
		}
		shares := distribute(current, int64(d.NewCPULimit))
		for i := range containers {
			ensureLimits(&containers[i])
			containers[i].Resources.Limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(shares[i], resource.DecimalSI)
		}
	}

	if d.NewMemLimit > 0 {
		current := make([]int64, len(containers))
		for i := range containers {
			// Work in MB (not bytes) so total*current stays inside int64 range.
			current[i] = containers[i].Resources.Limits.Memory().Value() / (1024 * 1024)
		}
		shares := distribute(current, int64(d.NewMemLimit))
		for i := range containers {
			ensureLimits(&containers[i])
			containers[i].Resources.Limits[corev1.ResourceMemory] = *resource.NewQuantity(shares[i]*1024*1024, resource.BinarySI)
		}
	}
	return nil
}

// ensureLimits makes sure a container has a non-nil Limits map before we write to
// it — a container may have had no limits set at all.
func ensureLimits(c *corev1.Container) {
	if c.Resources.Limits == nil {
		c.Resources.Limits = corev1.ResourceList{}
	}
}

// distribute splits total across len(current) buckets in proportion to each
// bucket's current value, returning integer shares that sum exactly to total.
// When every current value is 0 (no container has a limit set), it splits total
// evenly. Any remainder from integer division is given to the last bucket so the
// shares always sum back to total, whether scaling up or down.
//
// Callers pass CPU in millicores and memory in MB (not bytes) so that
// total*current[i] stays well within int64 range.
func distribute(current []int64, total int64) []int64 {
	out := make([]int64, len(current))
	if len(current) == 0 {
		return out
	}

	var sum int64
	for _, c := range current {
		sum += c
	}

	var allocated int64
	if sum > 0 {
		for i, c := range current {
			out[i] = total * c / sum
			allocated += out[i]
		}
	} else {
		base := total / int64(len(current))
		for i := range out {
			out[i] = base
			allocated += base
		}
	}

	// Hand the rounding remainder to the last bucket so the shares sum to total.
	out[len(out)-1] += total - allocated
	return out
}

// scaleDown reduces the owning workload by one replica, never below one. The
// floor matters: scaling a workload to zero is an outage, not an optimisation, so
// the executor refuses to do it even if asked. Deployments, StatefulSets, and
// bare ReplicaSets are scalable; a DaemonSet runs one pod per node and has no
// replica count, so scaling it is rejected outright.
//
// Runs under RetryOnConflict so a concurrent modification (e.g. an HPA touching
// the same object) yields a 409 rather than a lost update. The replica floor is
// re-checked against the freshly fetched object on every attempt, so if another
// actor already scaled it to 1 in between, we refuse instead of racing it to zero.
func (e *K8sExecutor) scaleDown(ctx context.Context, d *types.Decision) error {
	w, err := e.owningWorkload(ctx, d.Namespace, d.TargetPod)
	if err != nil {
		return err
	}
	apps := e.client.AppsV1()
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		switch w.Kind {
		case "Deployment":
			obj, err := apps.Deployments(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			next, err := decrementedReplicas(obj.Spec.Replicas, w)
			if err != nil {
				return err
			}
			obj.Spec.Replicas = &next
			_, err = apps.Deployments(w.Namespace).Update(ctx, obj, metav1.UpdateOptions{})
			return err
		case "StatefulSet":
			obj, err := apps.StatefulSets(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			next, err := decrementedReplicas(obj.Spec.Replicas, w)
			if err != nil {
				return err
			}
			obj.Spec.Replicas = &next
			_, err = apps.StatefulSets(w.Namespace).Update(ctx, obj, metav1.UpdateOptions{})
			return err
		case "ReplicaSet":
			obj, err := apps.ReplicaSets(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			next, err := decrementedReplicas(obj.Spec.Replicas, w)
			if err != nil {
				return err
			}
			obj.Spec.Replicas = &next
			_, err = apps.ReplicaSets(w.Namespace).Update(ctx, obj, metav1.UpdateOptions{})
			return err
		case "DaemonSet":
			return fmt.Errorf("scale_down: daemonset %s/%s runs one pod per node and has no replica count to reduce", w.Namespace, w.Name)
		default:
			return unsupportedWorkload("scale_down", w, d.TargetPod)
		}
	})
}

// decrementedReplicas returns replicas-1, enforcing the hard floor of one. A nil
// pointer is treated as the Kubernetes default of one replica. It refuses (rather
// than clamps) when already at the floor so the caller reports a clear failure
// instead of silently no-op'ing.
func decrementedReplicas(replicas *int32, w workloadRef) (int32, error) {
	current := int32(1)
	if replicas != nil {
		current = *replicas
	}
	if current <= 1 {
		return 0, fmt.Errorf("%s %s/%s already at %d replica(s); refusing to scale below 1", w.Kind, w.Namespace, w.Name, current)
	}
	return current - 1, nil
}

// cordonNode marks a node unschedulable so the scheduler stops placing new pods
// on it. Existing pods stay put; cordon only affects future scheduling.
//
// Runs under RetryOnConflict: node objects are updated frequently (kubelet status,
// taints, other controllers), so a bare Get/Update loses the race often. The
// already-cordoned short-circuit is re-evaluated per attempt, so a concurrent
// cordon resolves to a clean no-op instead of looping.
func (e *K8sExecutor) cordonNode(ctx context.Context, d *types.Decision) error {
	if d.TargetNode == "" {
		return fmt.Errorf("cordon_node requires a target node")
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		n, err := e.client.CoreV1().Nodes().Get(ctx, d.TargetNode, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if n.Spec.Unschedulable {
			return nil // already cordoned, nothing to do
		}
		n.Spec.Unschedulable = true
		_, err = e.client.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{})
		return err
	})
}

// evict removes the target pod so its owning controller recreates it, and the
// scheduler places the replacement on whichever node has room. We deliberately
// do not pin the replacement to d.TargetNode: forcing a specific node requires
// affinity/binding changes that are beyond the scope of a single eviction and
// can wedge the scheduler. The "move" is therefore best-effort.
//
// We submit an Eviction (policy/v1), not a raw Pod delete. The Eviction API
// routes through the API server's disruption controller, which honors any
// PodDisruptionBudget guarding the pod — the whole point of eviction is to not
// take a service below its safe minimum. A raw Delete would bypass that
// protection entirely.
func (e *K8sExecutor) evict(ctx context.Context, d *types.Decision) error {
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{Name: d.TargetPod, Namespace: d.Namespace},
	}
	err := e.client.CoreV1().Pods(d.Namespace).EvictV1(ctx, eviction)
	switch {
	case err == nil:
		return nil
	case apierrors.IsNotFound(err):
		return nil // pod already gone — the goal state is already met
	case apierrors.IsTooManyRequests(err):
		// 429 means a PodDisruptionBudget is protecting this pod right now. We
		// respect the budget and refuse to force it — never fall back to a raw
		// Delete. Surface a clear, distinguishable failure so the decision is
		// marked failed and the agent re-evaluates on its next cycle.
		return fmt.Errorf("eviction of %s/%s blocked by PodDisruptionBudget: %w", d.Namespace, d.TargetPod, err)
	default:
		return err
	}
}

// workloadRef identifies the top-level controller that owns a pod — the object
// whose spec actually governs the pod's limits and replica count. Kind is the
// Kubernetes kind ("Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", or
// something a CRD controls like "Rollout").
type workloadRef struct {
	Kind      string
	Namespace string
	Name      string
}

// owningWorkload resolves the top-level controller behind a pod by walking its
// owner references. A Deployment-managed pod is owned by a ReplicaSet which is in
// turn owned by the Deployment, so we hop through the ReplicaSet; StatefulSets and
// DaemonSets own their pods directly. A ReplicaSet with no controller of its own
// is a bare ReplicaSet (its own top-level workload), and a ReplicaSet owned by a
// CRD (e.g. an Argo Rollout) surfaces that CRD's kind so the caller can report a
// clear "unsupported" error rather than a misleading one.
func (e *K8sExecutor) owningWorkload(ctx context.Context, ns, podName string) (workloadRef, error) {
	pod, err := e.client.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return workloadRef{}, fmt.Errorf("get pod %s/%s: %w", ns, podName, err)
	}

	owner := controllerOf(pod.OwnerReferences)
	if owner == nil {
		return workloadRef{}, fmt.Errorf("pod %s/%s has no controlling workload (bare pod); nothing to act on", ns, podName)
	}

	// A ReplicaSet is an intermediary — hop to whatever controls it (usually a
	// Deployment). If nothing controls it, the ReplicaSet itself is the workload.
	if owner.Kind == "ReplicaSet" {
		rs, err := e.client.AppsV1().ReplicaSets(ns).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return workloadRef{}, fmt.Errorf("get replicaset %s/%s: %w", ns, owner.Name, err)
		}
		if rsOwner := controllerOf(rs.OwnerReferences); rsOwner != nil {
			return workloadRef{Kind: rsOwner.Kind, Namespace: ns, Name: rsOwner.Name}, nil
		}
		return workloadRef{Kind: "ReplicaSet", Namespace: ns, Name: owner.Name}, nil
	}

	return workloadRef{Kind: owner.Kind, Namespace: ns, Name: owner.Name}, nil
}

// controllerOf returns the managing controller among a set of owner references.
// It prefers the reference explicitly marked Controller=true (what Kubernetes
// stamps on real objects); if none is marked — e.g. a hand-built object in a test
// — it falls back to the first reference so callers still resolve a workload.
func controllerOf(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	if len(refs) > 0 {
		return &refs[0]
	}
	return nil
}

// unsupportedWorkload builds the error returned when an action targets a workload
// kind the executor cannot mutate (a CRD controller like an Argo Rollout, or a
// bare pod whose kind we do not recognise).
func unsupportedWorkload(action string, w workloadRef, podName string) error {
	return fmt.Errorf("%s: unsupported workload kind %q for pod %s/%s (owned by %s %s)", action, w.Kind, w.Namespace, podName, w.Kind, w.Name)
}
