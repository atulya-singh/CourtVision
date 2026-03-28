package metrics

import (
	"fmt"

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
