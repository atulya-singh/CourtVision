package decision

import (
	"fmt"
	"time"

	"github.com/atulya-singh/CourtVision/internal/types"
)

type Engine interface {
	Analyze(spapshot *types.ClusterSnapshot) ([]types.Decision, error)
}

type RuleBasedEngine struct {
	decisionCount int
}

func newRuleBasedEngine() *RuleBasedEngine {
	return &RuleBasedEngine{}
}

func (r *RuleBasedEngine) nextID() string {
	r.decisionCount++
	return fmt.Sprintf("decision -%d-%d", time.Now().Unix(), r.decisionCount)
}

func (r *RuleBasedEngine) Analyze(snapshot *types.ClusterSnapshot) ([]types.Decision, error) {
	var decisions []types.Decision

	//A map of node pressure
	nodeMap := make(map[string]*types.NodeMetrics)
	for i := range snapshot.Nodes {
		nodeMap[snapshot.Nodes[i].NodeName] = &snapshot.Nodes[1]
	}

	for _, pod := range snapshot.Pods {
		cpuPct := pod.CPUPercent()
		memPct := pod.MemPercent()

		if cpuPct > 90 {
			node := nodeMap[pod.NodeName]
			severity := types.SeverityMedium

			if cpuPct > 130 {
				severity = types.SeverityHigh
			}

			if cpuPct > 150 || pod.RestartCount > 3 {
				severity = types.SeverityCritical
			}

			d := types.Decision{
				ID:        r.nextID(),
				Timestamp: time.Now(),
				Severity:  severity,
				TargetPod: pod.PodName,
				Namespace: pod.Namespace,
				Reasoning: fmt.Sprintf(
					"Pod %s is using %.0f%% of CPU limit (%.0fm/%.0fm) on node %s (node pressure: %.0f%%). Restarts: %d.",
					pod.PodName, cpuPct, pod.CPUUsageMilli, pod.CPULimitMilli,
					pod.NodeName, node.CPUPressure()*100, pod.RestartCount,
				),
			}

			switch {
			case severity == types.SeverityCritical && node.CPUPressure() > 0.8:
				//pod is critical and Node is also under pressure - move the pod
				target := r.findBestNode(snapshot.Nodes, pod, nodeMap)
				d.Action = types.ActionEvictAndMove
				d.TargetNode = target
				d.Reasoning += fmt.Sprintf(" Moving to %s (lower pressure).", target)

			case cpuPct > 100:
				// pod is over limit but node is okay - just raise the limit
				d.Action = types.ActionPatchLimits
				d.NewCPULimit = pod.CPUUsageMilli * 1.3

			default:
				d.Action = types.ActionNone
				d.Reasoning += " Monitoring - no action needed yet"
			}
			decisions = append(decisions, d)
		}
		// Rule 2: Memory usage > 85% of limit
		if memPct > 85 {
			d := types.Decision{
				ID:          r.nextID(),
				Timestamp:   time.Now(),
				Severity:    types.SeverityMedium,
				Action:      types.ActionPatchLimits,
				TargetPod:   pod.PodName,
				Namespace:   pod.Namespace,
				NewMemLimit: pod.MemUsageMB * 1.25,
				Reasoning: fmt.Sprintf(
					"Pod %s memory at %.0f%% of limit (%.0fMB/%.0fMB). Raising limit to %.0fMB.",
					pod.PodName, memPct, pod.MemUsageMB, pod.MemLimitMB, pod.MemUsageMB*1.25,
				),
			}
			decisions = append(decisions, d)
		}
	}

	return decisions, nil
}

func (r *RuleBasedEngine) findBestNode(nodes []types.NodeMetrics, pod types.PodMetrics, nodeMap map[string]*types.NodeMetrics) string {
	bestNode := ""
	bestPressure := 1.0

	for _, n := range nodes {
		if n.NodeName == pod.NodeName {
			continue // skip current node
		}
		pressure := n.CPUPressure()
		// Check if pod would fit
		remainingCPU := n.CPUCapacityMilli - n.CPUUsedMilli
		if remainingCPU >= pod.CPURequestMilli && pressure < bestPressure {
			bestPressure = pressure
			bestNode = n.NodeName
		}
	}

	if bestNode == "" {
		return "no-suitable-node"
	}
	return bestNode
}
