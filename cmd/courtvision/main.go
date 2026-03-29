package main

import (
	"fmt"
	"os"

	"github.com/atulya-singh/CourtVision/internal/ui"
	"github.com/charmbracelet/lipgloss"
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
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(ui.Banner())
			fmt.Println()
			fmt.Println(ui.SubtitleStyle.Render("  Agentic Infrastructure Monitor"))
			fmt.Println(ui.DimStyle.Render(fmt.Sprintf("  %s (commit: %s)", version, commit)))
			fmt.Println()

			headerStyle := lipgloss.NewStyle().
				Foreground(ui.Purple).
				Bold(true)

			descStyle := lipgloss.NewStyle().
				Foreground(ui.Gray)

			fmt.Println(headerStyle.Render("  Available Commands:"))
			fmt.Println()

			commands := []struct{ name, desc string }{
				{"monitor", "Start the monitoring agent with API server and dashboard"},
				{"analyze", "Run a one-shot cluster analysis and print results"},
				{"status", "Check connectivity to Ollama and Kubernetes"},
				{"version", "Print version information"},
			}

			nameStyle := lipgloss.NewStyle().
				Foreground(ui.Cyan).
				Bold(true).
				Width(12)

			for _, c := range commands {
				fmt.Printf("    %s %s\n",
					nameStyle.Render(c.name),
					descStyle.Render(c.desc),
				)
			}

			fmt.Println()
			fmt.Println(ui.DimStyle.Render("  Use \"courtvision [command] --help\" for more information about a command."))
			fmt.Println()
		},
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
			fmt.Printf("%s %s\n", ui.BrandStyle.Render("CourtVision"), version)
			fmt.Printf("  %s %s\n", ui.DimStyle.Render("Commit:"), commit)
			fmt.Printf("  %s %s\n", ui.DimStyle.Render("Built: "), date)
		},
	}
}
