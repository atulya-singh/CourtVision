package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/atulya-singh/CourtVision/internal/llm"
	"github.com/atulya-singh/CourtVision/internal/metrics"
	"github.com/atulya-singh/CourtVision/internal/types"
	"github.com/spf13/cobra"
)

func analyzeCmd() *cobra.Command {
	var (
		ollamaURL  string
		model      string
		metricsStr string
		output     string
		namespace  string
	)

	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Run a one-shot cluster analysis and print results",
		Long: `Collect a single metrics snapshot, send it to the LLM for
analysis, and print the decisions. Then exit.
 
This is useful for quick checks without starting the full
monitoring agent. Like running "kubectl top pods" but with
AI-powered analysis.`,

		RunE: func(cmd *cobra.Command, args []string) error {
			log.SetFlags(0) // no time stamps for one shot output
			log.SetPrefix("")

			// 1. Create metrics provider
			var provider metrics.Provider
			switch metricsStr {
			case "mock":
				provider = metrics.NewMockProvider()
			case "k8s":
				p, err := metrics.NewK8sProvider(namespace)
				if err != nil {
					return fmt.Errorf("failed to connect to cluster: %w", err)
				}
				provider = p
			default:
				return fmt.Errorf("unknown metrics source: %s", metricsStr)
			}

			// 2. collect one snapshot
			snapshot, err := provider.GetClusterSnapshot()
			if err != nil {
				return fmt.Errorf("failed to collect metrics: %w", err)
			}

			// 3. Analyze with LLM
			llmClient := llm.NewClient(ollamaURL, model)
			engine := llm.NewEngine(llmClient)
			decisions, err := engine.Analyze(snapshot)
			if err != nil {
				return fmt.Errorf("analysis failed: %w", err)
			}

			// 4. Output results
			switch output {
			case "json":
				return printJSON(decisions)
			case "table":
				return printTable(decisions)
			default:
				return fmt.Errorf("unknown output format: %s (use 'json' or 'table')", output)
			}
		},
	}
	// Default values for the variables if user doesnt enter anything
	cmd.Flags().StringVar(&ollamaURL, "ollama-url", "http://localhost:11434", "Ollama server URL")
	cmd.Flags().StringVar(&model, "model", "llama3", "LLM model name")
	cmd.Flags().StringVar(&metricsStr, "metrics", "mock", "Metrics source (mock or k8s)")
	cmd.Flags().StringVar(&output, "output", "table", "Output format (table or json)")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace to watch (empty = all)")

	return cmd
}

// printJSON outputs decisions as pretty-printed JSON
func printJSON(decisions []types.Decision) error {
	data, err := json.MarshalIndent(decisions, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to format JSON: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

func printTable(decisions []types.Decision) error {
	fmt.Println()
	fmt.Printf("  %-10s %-25s %-15s %s\n", "SEVERITY", "POD", "ACTION", "REASONING")
	fmt.Printf("  %-10s %-25s %-15s %s\n",
		strings.Repeat("─", 10),
		strings.Repeat("─", 25),
		strings.Repeat("─", 15),
		strings.Repeat("─", 50),
	)

	for _, d := range decisions {
		reasoning := d.Reasoning
		if len(reasoning) > 50 {
			reasoning = reasoning[:47] + "..."
		}
		fmt.Printf("  %-10s %-25s %-15s %s\n",
			d.Severity, d.TargetPod, d.Action, reasoning)
	}

	fmt.Println()
	return nil
}
