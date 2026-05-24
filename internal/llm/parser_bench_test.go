package llm

import "testing"

var sampleLLMOutput = `{"action":"patch_limits","target_pod":"data-pipeline-1a7c","namespace":"production","severity":"critical","reasoning":"Pod consuming 220% of CPU limit (1100m/500m) with 7 restarts. Immediate limit increase required.","new_cpu_limit":1430}
{"action":"patch_limits","target_pod":"api-server-7d4f","namespace":"production","severity":"high","reasoning":"CPU at 96% of limit (480m/500m), approaching throttle threshold.","new_cpu_limit":624}
{"action":"none","target_pod":"worker-queue-5f8d","namespace":"production","severity":"low","reasoning":"Memory at 94% of limit but CPU normal. Monitoring."}
{"action":"none","target_pod":"feature-store-2d9f","namespace":"ml-workloads","severity":"medium","reasoning":"CPU at 98% of limit. Elevated but stable."}`

func BenchmarkParseResponse(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = ParseResponse(sampleLLMOutput)
	}
}
