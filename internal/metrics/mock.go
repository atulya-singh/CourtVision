package metrics

import (
	"math"
	"math/rand"
	"sync"
	"time"

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

func (m *MockProvider) GetClusterSnapshot() (*types.ClusterSnapshot, error) {
	m.mu.Lock()
	m.tick++
	tick := m.tick
	m.mu.Unlock()

	now := time.Now()
	snapshot := &types.ClusterSnapshot{Timestamp: now}

	nodeUsage := make(map[string][2]float64) //node -> [cpuUsed, memUsed]

	for _, pt := range m.pods {
		pm := types.PodMetrics{
			PodName:         pt.name,
			Namespace:       pt.namespace,
			NodeName:        pt.node,
			CPULimitMilli:   pt.cpuLimit,
			CPURequestMilli: pt.cpuReq,
			MemLimitMB:      pt.memLimit,
			MemRequestMB:    pt.memReq,
			Timestamp:       now,
		}

		jitter := 0.9 + rand.Float64()*0.2

		if pt.noisy {
			spike := math.Sin(float64(tick)/6.0)*0.5 + 0.5 //0.0 to 1.0
			cpuMultiplier := 1.0 + spike*1.5
			pm.CPUUsageMilli = pt.baseCPU * cpuMultiplier * jitter
			pm.MemUsageMB = pt.baseMem * (1.0 + spike*0.8) * jitter

			if pm.CPUUsageMilli > pt.cpuLimit*1.5 {
				pm.RestartCount = tick / 15
			}
		} else {
			pm.CPUUsageMilli = pt.baseCPU * jitter
			pm.MemUsageMB = pt.baseMem * jitter
		}

		snapshot.Pods = append(snapshot.Pods, pm)

		//Accumulate node usage
		u := nodeUsage[pt.node]
		u[0] += pm.CPUUsageMilli
		u[1] += pm.MemUsageMB
		nodeUsage[pt.node] = u
	}

	for _, nt := range m.nodes {
		usage := nodeUsage[nt.name]
		podCount := 0
		for _, pt := range m.pods {
			if pt.node == nt.name {
				podCount++
			}
		}

		nm := types.NodeMetrics{
			NodeName:         nt.name,
			NodeType:         nt.nodeType,
			CPUCapacityMilli: nt.cpuCap,
			CPUUsedMilli:     usage[0],
			MemCapacityMb:    nt.memCap,
			MemUsedMB:        usage[1],
			PodCount:         podCount,
			Timestamp:        now,
		}
		snapshot.Nodes = append(snapshot.Nodes, nm)
	}
	return snapshot, nil
}
