package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/atulya-singh/CourtVision/internal/llm"
	"github.com/atulya-singh/CourtVision/internal/metrics"
	"github.com/atulya-singh/CourtVision/internal/types"
	"github.com/atulya-singh/CourtVision/internal/ui"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

// ── Bubbletea model for the analysis spinner ───────────────────────────────

type analysisResult struct {
	decisions []types.Decision
	err       error
	elapsed   time.Duration
}

type analyzeModel struct {
	spinner  spinner.Model
	provider metrics.Provider
	engine   *llm.Engine
	output   string
	result   *analysisResult
	quitting bool
}

func (m analyzeModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.runAnalysis())
}

func (m analyzeModel) runAnalysis() tea.Cmd {
	return func() tea.Msg {
		start := time.Now()

		snapshot, err := m.provider.GetClusterSnapshot()
		if err != nil {
			return analysisResult{err: fmt.Errorf("failed to collect metrics: %w", err), elapsed: time.Since(start)}
		}

		decisions, err := m.engine.Analyze(snapshot)
		if err != nil {
			return analysisResult{err: fmt.Errorf("analysis failed: %w", err), elapsed: time.Since(start)}
		}

		return analysisResult{decisions: decisions, elapsed: time.Since(start)}
	}
}

func (m analyzeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
	case analysisResult:
		m.result = &msg
		m.quitting = true
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m analyzeModel) View() string {
	if m.result != nil {
		return ""
	}
	return fmt.Sprintf("\n  %s %s\n",
		m.spinner.View(),
		ui.DimStyle.Render("Analyzing cluster..."))
}

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
			log.SetFlags(0)
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

			// 2. Create LLM engine
			llmClient := llm.NewClient(ollamaURL, model)
			engine := llm.NewEngine(llmClient)

			// 3. Run with spinner
			s := spinner.New()
			s.Spinner = spinner.Dot
			s.Style = ui.BrandStyle

			m := analyzeModel{
				spinner:  s,
				provider: provider,
				engine:   engine,
				output:   output,
			}

			p := tea.NewProgram(m)
			finalModel, err := p.Run()
			if err != nil {
				return fmt.Errorf("spinner error: %w", err)
			}

			final := finalModel.(analyzeModel)
			if final.result == nil {
				return nil
			}
			if final.result.err != nil {
				return final.result.err
			}

			// 4. Output results
			switch output {
			case "json":
				return printJSON(final.result.decisions)
			case "table":
				return printStyledTable(final.result.decisions, metricsStr, final.result.elapsed)
			default:
				return fmt.Errorf("unknown output format: %s (use 'json' or 'table')", output)
			}
		},
	}

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

func printStyledTable(decisions []types.Decision, source string, elapsed time.Duration) error {
	fmt.Println()

	// Header
	header := fmt.Sprintf("  %-14s %-27s %-17s %s",
		ui.BoldStyle.Render("SEVERITY"),
		ui.BoldStyle.Render("POD"),
		ui.BoldStyle.Render("ACTION"),
		ui.BoldStyle.Render("REASONING"))
	fmt.Println(header)
	fmt.Printf("  %s %s %s %s\n",
		ui.DimStyle.Render(strings.Repeat("─", 12)),
		ui.DimStyle.Render(strings.Repeat("─", 25)),
		ui.DimStyle.Render(strings.Repeat("─", 15)),
		ui.DimStyle.Render(strings.Repeat("─", 50)),
	)

	for _, d := range decisions {
		reasoning := d.Reasoning
		if len(reasoning) > 50 {
			reasoning = reasoning[:47] + "..."
		}
		fmt.Printf("  %-14s %-27s %-17s %s\n",
			ui.SeverityBadge(string(d.Severity)),
			ui.CyanStyle.Render(d.TargetPod),
			ui.BlueStyle.Render(string(d.Action)),
			reasoning)
	}

	// Summary
	fmt.Println()
	summary := fmt.Sprintf("  Found %d issues in %s %s",
		len(decisions),
		source,
		ui.DimStyle.Render(fmt.Sprintf("(analyzed in %.1fs)", elapsed.Seconds())))
	fmt.Println(summary)
	fmt.Println()

	return nil
}
