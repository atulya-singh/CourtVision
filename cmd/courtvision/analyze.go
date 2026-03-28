package main

import (
	"github.com/spf13/cobra"
)

func analyzeCmd() *cobra.Command {
	var (
		ollamaURL  string
		model      string
		metricsStr string
		output     string
	)

	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Run a one-shot cluster analysis and print results",
		Long: `Collect a single metrics snapshot, send it to the LLM for
analysis, and print the decisions. Then exit.
 
This is useful for quick checks without starting the full
monitoring agent. Like running "kubectl top pods" but with
AI-powered analysis.`,
	}
}
