package executor

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/atulya-singh/CourtVision/internal/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
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
// metrics provider does. It does not talk to the cluster yet; failures here are
// only about loading credentials.
func NewK8sExecutor() (*K8sExecutor, error) {
	home := homedir.HomeDir()
	if home == "" {
		return nil, fmt.Errorf("could not find home directory")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", filepath.Join(home, ".kube", "config"))
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
// Deployment instead; Kubernetes then rolls out a new pod with the new limits.
func (e *K8sExecutor) patchLimits(ctx context.Context, d *types.Decision) error {
	dep, err := e.owningDeployment(ctx, d.Namespace, d.TargetPod)
	if err != nil {
		return err
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("deployment %s has no containers to patch", dep.Name)
	}

	c := &dep.Spec.Template.Spec.Containers[0]
	if c.Resources.Limits == nil {
		c.Resources.Limits = corev1.ResourceList{}
	}
	if d.NewCPULimit > 0 {
		c.Resources.Limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(int64(d.NewCPULimit), resource.DecimalSI)
	}
	if d.NewMemLimit > 0 {
		c.Resources.Limits[corev1.ResourceMemory] = *resource.NewQuantity(int64(d.NewMemLimit)*1024*1024, resource.BinarySI)
	}

	_, err = e.client.AppsV1().Deployments(dep.Namespace).Update(ctx, dep, metav1.UpdateOptions{})
	return err
}

// scaleDown reduces the owning Deployment by one replica, never below one. The
// floor matters: scaling a Deployment to zero is an outage, not an optimisation,
// so the executor refuses to do it even if asked.
func (e *K8sExecutor) scaleDown(ctx context.Context, d *types.Decision) error {
	dep, err := e.owningDeployment(ctx, d.Namespace, d.TargetPod)
	if err != nil {
		return err
	}

	current := int32(1)
	if dep.Spec.Replicas != nil {
		current = *dep.Spec.Replicas
	}
	if current <= 1 {
		return fmt.Errorf("deployment %s already at %d replica(s); refusing to scale below 1", dep.Name, current)
	}

	next := current - 1
	dep.Spec.Replicas = &next
	_, err = e.client.AppsV1().Deployments(dep.Namespace).Update(ctx, dep, metav1.UpdateOptions{})
	return err
}

// cordonNode marks a node unschedulable so the scheduler stops placing new pods
// on it. Existing pods stay put; cordon only affects future scheduling.
func (e *K8sExecutor) cordonNode(ctx context.Context, d *types.Decision) error {
	if d.TargetNode == "" {
		return fmt.Errorf("cordon_node requires a target node")
	}
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
}

// evict deletes the target pod. Its owning controller recreates it, and the
// scheduler places the replacement on whichever node has room. We deliberately
// do not pin the replacement to d.TargetNode: forcing a specific node requires
// affinity/binding changes that are beyond the scope of a single eviction and
// can wedge the scheduler. The "move" is therefore best-effort.
func (e *K8sExecutor) evict(ctx context.Context, d *types.Decision) error {
	return e.client.CoreV1().Pods(d.Namespace).Delete(ctx, d.TargetPod, metav1.DeleteOptions{})
}

// owningDeployment walks the ownership chain Pod -> ReplicaSet -> Deployment.
// Almost every Deployment-managed pod looks like "web-7d4f-abc12": the pod is
// owned by a ReplicaSet, which is owned by a Deployment. We need the Deployment
// because that is the object whose spec actually controls limits and replicas.
func (e *K8sExecutor) owningDeployment(ctx context.Context, ns, podName string) (*appsv1.Deployment, error) {
	pod, err := e.client.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", ns, podName, err)
	}

	rsName := ownerOfKind(pod.OwnerReferences, "ReplicaSet")
	if rsName == "" {
		return nil, fmt.Errorf("pod %s is not managed by a ReplicaSet; no Deployment to change", podName)
	}
	rs, err := e.client.AppsV1().ReplicaSets(ns).Get(ctx, rsName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get replicaset %s: %w", rsName, err)
	}

	depName := ownerOfKind(rs.OwnerReferences, "Deployment")
	if depName == "" {
		return nil, fmt.Errorf("replicaset %s is not managed by a Deployment", rsName)
	}
	dep, err := e.client.AppsV1().Deployments(ns).Get(ctx, depName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get deployment %s: %w", depName, err)
	}
	return dep, nil
}

func ownerOfKind(refs []metav1.OwnerReference, kind string) string {
	for _, r := range refs {
		if r.Kind == kind {
			return r.Name
		}
	}
	return ""
}
