package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "courtvision",
		Short: "Agentic Infrastructure Monitor",
		Long: `CourtVision — Agentic Infrastructure Monitor
 
An autonomous Kubernetes controller that uses a local LLM to analyze
cluster metrics and make intelligent infrastructure decisions.
 
Instead of blindly restarting pods, CourtVision reasons about the
problem and decides whether to adjust resource limits, move pods
to different nodes, or scale deployments.`,
	}

	rootCmd.AddCommand(monitorCmd())
	rootCmd.AddCommand(analyzeCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(versionCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("CourtVision %s\n", version)
			fmt.Printf("  Commit: %s\n", commit)
			fmt.Printf("  Built:  %s\n", date)
		},
	}
}
