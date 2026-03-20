package llm

import (
	"fmt"
	"strings"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// BuildPrompt creates the full prompt that gets sent to the LLM.
// The prompt has three parts:
//  1. System context — who you are and what you do
//  2. Cluster data — the actual metrics in a readable format
//  3. Output instructions — exactly how to format the response
func BuildPrompt(snapshot *types.ClusterSnapshot) string {
	var b strings.Builder

	// Part 1: System context
	b.WriteString(`You are an expert Kubernetes infrastructure agent. Your job is to analyze cluster metrics and decide what actions to take.

You understand resource contention, noisy neighbor problems, node affinity, and capacity planning. You make conservative decisions — only act when there's a clear problem.

`)

	// Part 2: Cluster data
	b.WriteString("=== CURRENT CLUSTER STATE ===\n\n")

	// Nodes first — gives the LLM context about available capacity
	b.WriteString("NODES:\n")
	for _, n := range snapshot.Nodes {
		cpuPressure := n.CPUPressure() * 100
		memPressure := 0.0
		if n.MemCapacityMb > 0 {
			memPressure = (n.MemUsedMB / n.MemCapacityMb) * 100
		}
		fmt.Fprintf(&b, "  %s [type=%s] CPU: %.0f/%.0fm (%.0f%%) | Mem: %.0f/%.0fMB (%.0f%%) | Pods: %d\n",
			n.NodeName, n.NodeType,
			n.CPUUsedMilli, n.CPUCapacityMilli, cpuPressure,
			n.MemUsedMB, n.MemCapacityMb, memPressure,
			n.PodCount,
		)
	}

	// Pods — the detailed per-pod metrics
	b.WriteString("\nPODS:\n")
	for _, p := range snapshot.Pods {
		cpuPct := p.CPUPercent()
		memPct := p.MemPercent()

		// Flag pods that look problematic
		flag := ""
		if cpuPct > 90 || memPct > 85 {
			flag = " ⚠️ ATTENTION"
		}
		if cpuPct > 130 || p.RestartCount > 3 {
			flag = " 🚨 CRITICAL"
		}

		fmt.Fprintf(&b, "  %s (ns=%s, node=%s)%s\n", p.PodName, p.Namespace, p.NodeName, flag)
		fmt.Fprintf(&b, "    CPU: %.0fm used / %.0fm limit / %.0fm request (%.0f%% of limit)\n",
			p.CPUUsageMilli, p.CPULimitMilli, p.CPURequestMilli, cpuPct)
		fmt.Fprintf(&b, "    Mem: %.0fMB used / %.0fMB limit / %.0fMB request (%.0f%% of limit)\n",
			p.MemUsageMB, p.MemLimitMB, p.MemRequestMB, memPct)
		if p.RestartCount > 0 {
			fmt.Fprintf(&b, "    Restarts: %d\n", p.RestartCount)
		}
	}

	// Part 3: Output instructions
	b.WriteString(`
=== YOUR TASK ===

Analyze the cluster state above. For each problem you find, output a JSON object on its own line.

AVAILABLE ACTIONS:
- "patch_limits": Change a pod's CPU or memory limits (use when pod is near/over limits but the node has capacity)
- "evict_and_move": Move a pod to a different node (use when the current node is under heavy pressure)
- "scale_down": Reduce replicas (use when a deployment is over-provisioned)
- "none": Monitor only (use when something looks elevated but isn't dangerous yet)

OUTPUT FORMAT — one JSON object per line, no other text:
{"action":"<action>","target_pod":"<pod_name>","namespace":"<namespace>","severity":"<low|medium|high|critical>","reasoning":"<1-2 sentence explanation>","target_node":"<node_name if evict_and_move>","new_cpu_limit":<millicores if patch_limits>,"new_mem_limit":<MB if patch_limits>}

RULES:
- Only flag pods that actually have problems (>90% CPU or >85% memory usage)
- If no problems exist, output exactly: {"action":"none","reasoning":"All pods operating within normal parameters"}
- Be specific in reasoning — mention actual numbers
- For patch_limits, set new limits to 130% of current usage (30% headroom)
- For evict_and_move, pick the node with the lowest CPU pressure that has enough capacity
- Output ONLY valid JSON lines, nothing else — no markdown, no explanation, no code blocks
`)

	return b.String()
}
