package main

import (
	"fmt"
	"os"

	"github.com/atulya-singh/CourtVision/internal/ui"
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

	// If no subcommand provided, launch interactive REPL
	if len(os.Args) < 2 {
		runREPL(rootCmd)
		return
	}

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
			fmt.Printf("%s %s\n", ui.BrandStyle.Render("CourtVision"), version)
			fmt.Printf("  %s %s\n", ui.DimStyle.Render("Commit:"), commit)
			fmt.Printf("  %s %s\n", ui.DimStyle.Render("Built: "), date)
		},
	}
}
