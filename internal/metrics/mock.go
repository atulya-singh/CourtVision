package metrics

import (
	"sync"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// Provider is the interface any metrics source must implement
type Provider interface {
	GetClusterSnapshot() (*types.ClusterSnapshot, error)
}

type MockProvider struct {
	mu    sync.Mutex
	tick  int
	nodes []nodeTemplate
	pods  []podTemplate
}

type nodeTemplate struct {
	name     string
	nodeType string
	cpuCap   float64 //millicores
	memCap   float64 //MB
}

type podTemplate struct {
	name      string
	namespace string
	node      string
	baseCPU   float64 //base CPU usage in millicores
	baseMem   float64 // base mem usage in MB
	cpuLimit  float64
	cpuReq    float64
	memLimit  float64
	memReq    float64
	noisy     bool // will this pode spike ?
}

func NewMockProvider() *MockProvider {
	nodes := []nodeTemplate{
		{"node-general-1", "general", 4000, 8192},
		{"node-general-2", "general", 4000, 8192},
		{"node-compute-1", "compute-optimized", 8000, 4096},
		{"node-memory-1", "memory-optimized", 2000, 16384},
	}

	pods := []podTemplate{
		// Normal well-behaved pods
		{"api-server-7d4f", "production", "node-general-1", 200, 256, 500, 250, 512, 256, false},
		{"auth-service-3b1a", "production", "node-general-1", 150, 128, 300, 200, 256, 128, false},
		{"cache-redis-9c2e", "production", "node-general-2", 100, 512, 200, 100, 1024, 512, false},
		{"worker-queue-5f8d", "production", "node-general-2", 300, 384, 500, 300, 512, 384, false},

		// THE NOISY NEIGHBOR — a data pipeline that periodically spikes
		{"data-pipeline-1a7c", "production", "node-general-1", 400, 300, 500, 400, 512, 300, true},

		// Pods on specialized nodes
		{"ml-training-8e3b", "ml-workloads", "node-compute-1", 2000, 1024, 4000, 2000, 2048, 1024, false},
		{"feature-store-2d9f", "ml-workloads", "node-compute-1", 500, 512, 1000, 500, 1024, 512, false},
		{"postgres-primary-6a1c", "databases", "node-memory-1", 400, 4096, 800, 400, 8192, 4096, false},
	}

	return &MockProvider{nodes: nodes, pods: pods}
}
