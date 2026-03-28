package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/atulya-singh/CourtVision/internal/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"

	"path/filepath"
)

type K8sProvider struct {
	coreClient    kubernetes.Interface
	metricsClient metricsv.Interface
	namespace     string
}

func NewK8sProvider(namespace string) (*K8sProvider, error) {
	home := homedir.HomeDir()
	if home == "" {
		return nil, fmt.Errorf("could not find home directory")
	}
	kubeconfigPath := filepath.Join(home, ".kube", "config")

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
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
	}, nil
}

func (k *K8sProvider) GetClusterSnapshot() (*types.ClusterSnapshot, error) {
	ctx := context.Background()
	now := time.Now()

	// List all nodes

	nodeList, err := k.coreClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	// List all pods
	podList, err := k.coreClient.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	// Get node metrics
	// returns the actual CPU and memory being used right now on each node
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

	snapshot := &types.ClusterSnapshot{Timestamp: now}

	// Counting total number of pods on each node
	podCountPerNode := make(map[string]int)
	for _, pod := range podList.Items {
		podCountPerNode[pod.Spec.NodeName]++
	}

	for _, node := range nodeList.Items {
		cpuCapacity := float64(node.Status.Allocatable.Cpu().MilliValue())
		memCapacity := float64(node.Status.Allocatable.Memory().Value()) / (1024 * 1024)

		usage := nodeUsageMap[node.Name]

		nodeType := "general"
		if instanceType, ok := node.Labels["node.kubernetes.io/instance-typ"]; ok {
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

	for _, pod := range podList.Items {
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
