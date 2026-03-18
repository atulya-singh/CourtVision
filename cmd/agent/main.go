package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/atulya-singh/CourtVision/internal/decision"
	"github.com/atulya-singh/CourtVision/internal/metrics"
	"github.com/atulya-singh/CourtVision/internal/types"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmsgprefix)
	log.SetPrefix("[CourtVision] ")

	log.Println("Starting Agentic Infrastructure Monitor")
	log.Println("---")
	//constructors
	provider := metrics.NewMockProvider()
	engine := decision.NewRuleBasedEngine()

	//decision log
	var allDecisions []types.Decision

	// Main monitoring loop
	interval := 5 * time.Second // slow interval
	if len(os.Args) > 1 && os.Args[1] == "--fast" {
		interval = 1 * time.Second // speed up the loop if the user asked.
	}
	ticker := time.NewTicker(interval) // creating a ticker that ticks between the specified interval
	defer ticker.Stop()

	maxCycles := 10
	cycle := 0

	for range ticker.C {
		cycle++
		log.Printf("=== Cycle %d/%d ===", cycle, maxCycles)

		snapshot, err := provider.GetClusterSnapshot()
		if err != nil {
			log.Printf("ERROR: %V", err)
			continue // skip to the next cycle
		}

		printClusterSummary(snapshot)

		decisions, err := engine.Analyze(snapshot)
		if err != nil {
			log.Printf("ERROR analyzing: %v", err)
			continue
		}

		if len(decisions) == 0 {
			log.Printf(" No issues detected")
		} else {
			for _, d := range decisions {
				printDecision(&d)
				allDecisions = append(allDecisions, d)
			}
		}
		fmt.Println()

		if cycle >= maxCycles {
			break
		}
	}
	log.Println("=== Session Summary ===")
	log.Printf("Total decisions: %d", len(allDecisions))

	actionCounts := make(map[types.ActionType]int)
	for _, d := range allDecisions {
		actionCounts[d.Action]++
	}
	for action, count := range actionCounts {
		log.Printf("  %s: %d", action, count)
	}

	// Dump decisions as JSON for later use
	if len(allDecisions) > 0 {
		data, _ := json.MarshalIndent(allDecisions, "", "  ")
		os.WriteFile("decisions.json", data, 0644)
		log.Println("Decisions written to decisions.json")
	}
}

func printClusterSummary(s *types.ClusterSnapshot) {
	for _, n := range s.Nodes {
		log.Printf("  Node %-20s [%s] CPU: %5.0f/%5.0fm (%.0f%%)  Mem: %5.0f/%5.0fMB  Pods: %d",
			n.NodeName, n.NodeType,
			n.CPUUsedMilli, n.CPUCapacityMilli, n.CPUPressure()*100,
			n.MemUsedMB, n.MemCapacityMb, n.PodCount,
		)
	}
}

func printDecision(d *types.Decision) {
	icon := "⚡"
	switch d.Severity {
	case types.SeverityCritical:
		icon = "🚨"
	case types.SeverityHigh:
		icon = "🔴"
	case types.SeverityMedium:
		icon = "🟡"
	case types.SeverityLow:
		icon = "🟢"
	}

	log.Printf("  %s [%s] %s → %s", icon, d.Severity, d.TargetPod, d.Action)
	log.Printf("    Reasoning: %s", d.Reasoning)
}
