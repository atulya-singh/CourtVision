package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/atulya-singh/CourtVision/internal/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"

	"path/filepath"
)

const (
	// snapshotTimeout bounds a full metrics collection. Without it the collect
	// loop uses context.Background() and a slow or unreachable API server can
	// hang the pipeline indefinitely; with it a stuck request fails fast and the
	// loop recovers on the next tick.
	snapshotTimeout = 30 * time.Second
	// listPageSize caps how many objects we pull per API request so a very large
	// cluster can't force one giant unbounded response into memory. The apiserver
	// returns a Continue token that we follow until the full set is drained.
	listPageSize = 500
)

type K8sProvider struct {
	coreClient    kubernetes.Interface
	metricsClient metricsv.Interface
	namespace     string
	clusterName   string
}

// restConfig loads the kubeconfig (~/.kube/config) and resolves the effective
// context name. contextName selects which context to target; "" falls back to
// the kubeconfig's current-context. Shared by NewK8sProvider and the lightweight
// connectivity probe so both speak the same kubeconfig vocabulary.
func restConfig(contextName string) (*rest.Config, string, error) {
	home := homedir.HomeDir()
	if home == "" {
		return nil, "", fmt.Errorf("could not find home directory")
	}
	kubeconfigPath := filepath.Join(home, ".kube", "config")

	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)

	config, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	// Resolve the effective context name so a caller relying on the
	// current-context still gets a meaningful cluster label.
	clusterName := contextName
	if clusterName == "" {
		if rawConfig, err := clientConfig.RawConfig(); err == nil {
			clusterName = rawConfig.CurrentContext
		}
	}
	return config, clusterName, nil
}

// NewK8sProvider builds a provider for a single cluster. contextName selects
// which kubeconfig context to target; passing "" falls back to the kubeconfig's
// current-context (the original single-cluster behavior). The resolved context
// name is stamped onto every snapshot so decisions can be attributed to the
// right cluster in a multi-cluster setup.
func NewK8sProvider(namespace, contextName string) (*K8sProvider, error) {
	config, clusterName, err := restConfig(contextName)
	if err != nil {
		return nil, err
	}

	coreClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	metricsClient, err := metricsv.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create metrics client: %w", err)
	}

	return &K8sProvider{
		coreClient:    coreClient,
		metricsClient: metricsClient,
		namespace:     namespace,
		clusterName:   clusterName,
	}, nil
}

// K8sStatus is the outcome of a lightweight API-server connectivity probe.
type K8sStatus struct {
	Context       string // resolved kubeconfig context ("" if none could be loaded)
	ServerVersion string // e.g. "v1.29.0" when reachable
	Reachable     bool
	Err           error // why the probe failed (nil when Reachable)
}

// CheckK8sConnectivity loads the kubeconfig (contextName "" = current-context)
// and pings the API server's /version endpoint under the given timeout. It is a
// cheap liveness probe — no metrics-server, no listing — used by `status` and
// the REPL banner to show whether Kubernetes is actually reachable. It never
// blocks longer than timeout and never panics: every failure is reported in the
// returned K8sStatus.
func CheckK8sConnectivity(contextName string, timeout time.Duration) K8sStatus {
	config, clusterName, err := restConfig(contextName)
	if err != nil {
		return K8sStatus{Context: clusterName, Err: err}
	}
	config.Timeout = timeout // bound the /version request

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return K8sStatus{Context: clusterName, Err: err}
	}
	return probeVersion(client, clusterName)
}

// probeVersion asks the discovery endpoint for the server version. Split from
// CheckK8sConnectivity so it can be unit-tested with a fake clientset.
func probeVersion(client kubernetes.Interface, clusterName string) K8sStatus {
	ver, err := client.Discovery().ServerVersion()
	if err != nil {
		return K8sStatus{Context: clusterName, Err: err}
	}
	return K8sStatus{Context: clusterName, ServerVersion: ver.String(), Reachable: true}
}

func (k *K8sProvider) GetClusterSnapshot() (*types.ClusterSnapshot, error) {
	// Bound the whole collection: a slow or unreachable API server must not hang
	// the collect loop forever. All the List calls below share this deadline.
	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()
	now := time.Now()

	// List all nodes (paginated: bounded memory per request on large clusters).
	nodeItems, err := listAllNodes(ctx, k.coreClient)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	// List all pods (paginated).
	podItems, err := listAllPods(ctx, k.coreClient, k.namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	// Get node metrics
	// returns the actual CPU and memory being used right now on each node.
	// metrics-server doesn't honor Continue tokens, so these two stay single
	// calls — but still under the bounded context above.
	nodeMetricsList, err := k.metricsClient.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get node metrics (is metrics-server running ?): %w", err)
	}

	// Get Pod metrics
	// returns the actual CPU and memory being used right now by each pod

	podMetricsList, err := k.metricsClient.MetricsV1beta1().PodMetricses(k.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod metrics (is metrics-server running?): %w", err)
	}

	nodeUsageMap := make(map[string][2]float64) // node name -> [cpuMillis, memMB]
	for _, nm := range nodeMetricsList.Items {
		cpuMillis := float64(nm.Usage.Cpu().MilliValue())
		memBytes := float64(nm.Usage.Memory().Value())
		memMB := memBytes / (1024 * 1024)

		nodeUsageMap[nm.Name] = [2]float64{cpuMillis, memMB}
	}

	podUsageMap := make(map[string][2]float64)
	for _, pm := range podMetricsList.Items {
		key := pm.Namespace + "/" + pm.Name

		var cpuMillis, memBytes float64
		for _, container := range pm.Containers {
			cpuMillis += float64(container.Usage.Cpu().MilliValue())
			memBytes += float64(container.Usage.Memory().Value())
		}
		memMB := memBytes / (1024 * 1024)

		podUsageMap[key] = [2]float64{cpuMillis, memMB}
	}

	snapshot := &types.ClusterSnapshot{ClusterName: k.clusterName, Timestamp: now}

	// Counting total number of pods on each node
	podCountPerNode := make(map[string]int)
	for _, pod := range podItems {
		podCountPerNode[pod.Spec.NodeName]++
	}

	for _, node := range nodeItems {
		cpuCapacity := float64(node.Status.Allocatable.Cpu().MilliValue())
		memCapacity := float64(node.Status.Allocatable.Memory().Value()) / (1024 * 1024)

		usage := nodeUsageMap[node.Name]

		nodeType := "general"
		if instanceType, ok := node.Labels["node.kubernetes.io/instance-type"]; ok {
			nodeType = instanceType
		}

		if instanceType, ok := node.Labels["beta.kubernetes.io/instance-type"]; ok {
			nodeType = instanceType
		}

		nm := types.NodeMetrics{
			NodeName:         node.Name,
			NodeType:         nodeType,
			CPUCapacityMilli: cpuCapacity,
			CPUUsedMilli:     usage[0],
			MemCapacityMb:    memCapacity,
			MemUsedMB:        usage[1],
			PodCount:         podCountPerNode[node.Name],
			Timestamp:        now,
		}
		snapshot.Nodes = append(snapshot.Nodes, nm)
	}

	for _, pod := range podItems {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		var cpuRequest, cpuLimit, memRequest, memLimit float64

		for _, container := range pod.Spec.Containers {
			if req, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
				cpuRequest += float64(req.MilliValue())
			}
			if req, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
				memRequest += float64(req.Value()) / (1024 * 1024)
			}
			if lim, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
				cpuLimit += float64(lim.MilliValue())
			}
			if lim, ok := container.Resources.Limits[corev1.ResourceMemory]; ok {
				memLimit += float64(lim.Value()) / (1024 * 1024)
			}
		}

		var totalRestarts int
		for _, cs := range pod.Status.ContainerStatuses {
			totalRestarts += int(cs.RestartCount)
		}

		key := pod.Namespace + "/" + pod.Name
		usage := podUsageMap[key]
		pm := types.PodMetrics{
			PodName:         pod.Name,
			Namespace:       pod.Namespace,
			NodeName:        pod.Spec.NodeName,
			CPUUsageMilli:   usage[0],
			CPULimitMilli:   cpuLimit,
			CPURequestMilli: cpuRequest,
			MemUsageMB:      usage[1],
			MemLimitMB:      memLimit,
			MemRequestMB:    memRequest,
			RestartCount:    totalRestarts,
			Timestamp:       now,
		}
		snapshot.Pods = append(snapshot.Pods, pm)
	}

	return snapshot, nil
}

// listAllNodes pages through every node, following the apiserver's Continue
// token so a large cluster is fetched in bounded chunks rather than one
// unbounded response.
func listAllNodes(ctx context.Context, client kubernetes.Interface) ([]corev1.Node, error) {
	var nodes []corev1.Node
	opts := metav1.ListOptions{Limit: listPageSize}
	for {
		page, err := client.CoreV1().Nodes().List(ctx, opts)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, page.Items...)
		if page.Continue == "" {
			return nodes, nil
		}
		opts.Continue = page.Continue
	}
}

// listAllPods pages through every pod in the namespace, following the Continue
// token the same way as listAllNodes.
func listAllPods(ctx context.Context, client kubernetes.Interface, namespace string) ([]corev1.Pod, error) {
	var pods []corev1.Pod
	opts := metav1.ListOptions{Limit: listPageSize}
	for {
		page, err := client.CoreV1().Pods(namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		pods = append(pods, page.Items...)
		if page.Continue == "" {
			return pods, nil
		}
		opts.Continue = page.Continue
	}
}
