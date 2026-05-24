package decision

import (
	"testing"
	"time"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// cluster mirrors the mock provider: 4 nodes, 8 pods, one noisy neighbor
var benchSnapshot = &types.ClusterSnapshot{
	Timestamp: time.Now(),
	Nodes: []types.NodeMetrics{
		{NodeName: "node-general-1", CPUCapacityMilli: 4000, CPUUsedMilli: 3200, MemCapacityMb: 8192, MemUsedMB: 6000},
		{NodeName: "node-general-2", CPUCapacityMilli: 4000, CPUUsedMilli: 1600, MemCapacityMb: 8192, MemUsedMB: 3000},
		{NodeName: "node-compute-1", CPUCapacityMilli: 8000, CPUUsedMilli: 2500, MemCapacityMb: 4096, MemUsedMB: 1500},
		{NodeName: "node-memory-1", CPUCapacityMilli: 2000, CPUUsedMilli: 400, MemCapacityMb: 16384, MemUsedMB: 4096},
	},
	Pods: []types.PodMetrics{
		{PodName: "api-server", Namespace: "production", NodeName: "node-general-1", CPUUsageMilli: 480, CPULimitMilli: 500, CPURequestMilli: 250, MemUsageMB: 490, MemLimitMB: 512},
		{PodName: "auth-service", Namespace: "production", NodeName: "node-general-1", CPUUsageMilli: 280, CPULimitMilli: 300, CPURequestMilli: 200, MemUsageMB: 240, MemLimitMB: 256},
		{PodName: "cache-redis", Namespace: "production", NodeName: "node-general-2", CPUUsageMilli: 190, CPULimitMilli: 200, CPURequestMilli: 100, MemUsageMB: 960, MemLimitMB: 1024},
		{PodName: "worker-queue", Namespace: "production", NodeName: "node-general-2", CPUUsageMilli: 460, CPULimitMilli: 500, CPURequestMilli: 300, MemUsageMB: 480, MemLimitMB: 512},
		// noisy neighbor — throttled, high restarts
		{PodName: "data-pipeline", Namespace: "production", NodeName: "node-general-1", CPUUsageMilli: 1100, CPULimitMilli: 500, CPURequestMilli: 400, MemUsageMB: 900, MemLimitMB: 512, RestartCount: 7},
		{PodName: "ml-training", Namespace: "ml-workloads", NodeName: "node-compute-1", CPUUsageMilli: 2100, CPULimitMilli: 4000, CPURequestMilli: 2000, MemUsageMB: 1900, MemLimitMB: 2048},
		{PodName: "feature-store", Namespace: "ml-workloads", NodeName: "node-compute-1", CPUUsageMilli: 980, CPULimitMilli: 1000, CPURequestMilli: 500, MemUsageMB: 1010, MemLimitMB: 1024},
		{PodName: "postgres-primary", Namespace: "databases", NodeName: "node-memory-1", CPUUsageMilli: 380, CPULimitMilli: 800, CPURequestMilli: 400, MemUsageMB: 7000, MemLimitMB: 8192},
	},
}

func BenchmarkRuleEngine_Analyze(b *testing.B) {
	engine := NewRuleBasedEngine()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = engine.Analyze(benchSnapshot)
	}
}

func BenchmarkRuleEngine_Analyze_Parallel(b *testing.B) {
	engine := NewRuleBasedEngine()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = engine.Analyze(benchSnapshot)
		}
	})
}
