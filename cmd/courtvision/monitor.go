package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/atulya-singh/CourtVision/internal/ui"
	"github.com/spf13/cobra"
	// These would be your actual import paths:
	"github.com/atulya-singh/CourtVision/internal/api"
	"github.com/atulya-singh/CourtVision/internal/decision"
	"github.com/atulya-singh/CourtVision/internal/llm"
	"github.com/atulya-singh/CourtVision/internal/metrics"
	"github.com/atulya-singh/CourtVision/internal/store"
)

func monitorCmd() *cobra.Command {
	var (
		port       string
		ollamaURL  string
		model      string
		metricsStr string
		namespace  string
		interval   time.Duration
		dryRun     bool
	)

	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Start the monitoring agent with API server and dashboard",
		Long: `Start the CourtVision monitoring agent. It continuously collects
cluster metrics, analyzes them with a local LLM, and serves a
real-time dashboard.

The agent runs a monitoring loop at the specified interval,
collecting metrics from the configured source and sending them
to the LLM for analysis. Decisions are served via an HTTP API
with SSE for real-time updates.`,

		RunE: func(cmd *cobra.Command, args []string) error {
			// Print banner
			fmt.Println(ui.Banner())
			fmt.Println()

			// Build config box
			var configLines []string
			configLines = append(configLines, ui.BrandStyle.Render("Configuration"))
			configLines = append(configLines, "")
			configLines = append(configLines, ui.ConfigLine("Metrics:", metricsStr))
			configLines = append(configLines, ui.ConfigLine("Ollama:", fmt.Sprintf("%s (model: %s)", ollamaURL, ui.CyanStyle.Render(model))))
			configLines = append(configLines, ui.ConfigLine("API port:", port))
			configLines = append(configLines, ui.ConfigLine("Interval:", interval.String()))

			if dryRun {
				configLines = append(configLines, ui.ConfigLine("Mode:", ui.DryRunBadge))
			} else {
				configLines = append(configLines, ui.ConfigLine("Mode:", ui.GreenStyle.Render("LIVE")))
			}

			fmt.Println(ui.ConfigBox.Render(strings.Join(configLines, "\n")))
			fmt.Println()

			// Set up styled logging
			log.SetFlags(0)
			log.SetPrefix("")

			styledLog := func(format string, args ...interface{}) {
				ts := ui.DimStyle.Render(time.Now().Format("15:04:05"))
				msg := fmt.Sprintf(format, args...)
				fmt.Printf("  %s  %s\n", ts, msg)
			}

			styledLog("Starting Agentic Infrastructure Monitor")
			styledLog("%s", ui.DimStyle.Render("───────────────────────────────────────"))

			// 1. Create the shared state store
			st := store.New()

			// 2. Choose metrics provider based on flag
			var provider metrics.Provider
			switch metricsStr {
			case "mock":
				styledLog("Using %s metrics provider", ui.CyanStyle.Render("mock"))
				provider = metrics.NewMockProvider()
			case "k8s":
				styledLog("Using %s metrics provider", ui.CyanStyle.Render("Kubernetes"))
				var err error
				provider, err = metrics.NewK8sProvider(namespace)
				if err != nil {
					return fmt.Errorf("failed to create k8s provider: %w", err)
				}
			default:
				return fmt.Errorf("unknown metrics source: %s (use 'mock' or 'k8s')", metricsStr)
			}

			// 3. Create the LLM engine
			llmClient := llm.NewClient(ollamaURL, model)
			engine := llm.NewEngine(llmClient)

			// 4. Start the monitoring loop in background
			go styledMonitorLoop(provider, engine, st, interval)

			// 5. Start the API server (blocks forever)
			server := api.NewServer(st, port)
			styledLog("API server listening on %s", ui.CyanStyle.Render(":"+port))
			return server.Start()
		},
	}

	cmd.Flags().StringVar(&port, "port", "8080", "API server port")
	cmd.Flags().StringVar(&ollamaURL, "ollama-url", "http://localhost:11434", "Ollama server URL")
	cmd.Flags().StringVar(&model, "model", "llama3", "LLM model name")
	cmd.Flags().StringVar(&metricsStr, "metrics", "mock", "Metrics source (mock or k8s)")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace to monitor (empty for all namespaces)")
	cmd.Flags().DurationVar(&interval, "interval", 3*time.Second, "Monitoring loop interval")
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "Log decisions without executing them")

	return cmd
}

func styledMonitorLoop(provider metrics.Provider, engine decision.Engine, st *store.Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	styledLog := func(format string, args ...interface{}) {
		ts := ui.DimStyle.Render(time.Now().Format("15:04:05"))
		msg := fmt.Sprintf(format, args...)
		fmt.Printf("  %s  %s\n", ts, msg)
	}

	styledLog("Monitor loop started")

	for range ticker.C {
		snapshot, err := provider.GetClusterSnapshot()
		if err != nil {
			styledLog("%s collecting metrics: %v", ui.RedStyle.Render("ERROR"), err)
			continue
		}

		st.SetSnapshot(snapshot)

		decisions, err := engine.Analyze(snapshot)
		if err != nil {
			styledLog("%s analyzing: %v", ui.RedStyle.Render("ERROR"), err)
			continue
		}

		for _, d := range decisions {
			st.AddDecision(d)
			styledLog("Decision: %s %s → %s",
				ui.SeverityBadge(string(d.Severity)),
				ui.CyanStyle.Render(d.TargetPod),
				ui.BlueStyle.Render(string(d.Action)))
		}

		if len(decisions) == 0 {
			styledLog("%s Cycle complete — no issues", ui.CheckMark)
		}
	}
}
