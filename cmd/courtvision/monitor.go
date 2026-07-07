package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/atulya-singh/CourtVision/internal/api"
	"github.com/atulya-singh/CourtVision/internal/decision"
	"github.com/atulya-singh/CourtVision/internal/llm"
	"github.com/atulya-singh/CourtVision/internal/metrics"
	"github.com/atulya-singh/CourtVision/internal/store"
	"github.com/atulya-singh/CourtVision/internal/types"
	"github.com/atulya-singh/CourtVision/internal/ui"
	"github.com/spf13/cobra"
)

func monitorCmd() *cobra.Command {
	var (
		port       string
		ollamaURL  string
		model      string
		metricsStr string
		namespace  string
		interval   time.Duration
	)

	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Start the monitoring agent with API server and dashboard",
		Long: `Start the CourtVision monitoring agent. It continuously collects
cluster metrics, analyzes them with a local LLM, and serves a
real-time dashboard.

The agent runs a monitoring loop at the specified interval,
collecting metrics from the configured source and sending them
to the LLM for analysis. Decisions are served via a read-only HTTP
API with SSE for real-time updates — the dashboard observes but never
mutates the cluster. To execute a decision, use 'analyze --apply', the
REPL review flow, or 'multi-monitor --auto-safe'.`,

		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

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

			configLines = append(configLines, ui.ConfigLine("Mode:", ui.CyanStyle.Render("observe-only")))

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
				provider = metrics.NewMockProvider("mock-cluster")
			case "k8s":
				styledLog("Using %s metrics provider", ui.CyanStyle.Render("Kubernetes"))
				var err error
				provider, err = metrics.NewK8sProvider(namespace, "")
				if err != nil {
					return fmt.Errorf("failed to create k8s provider: %w", err)
				}
			default:
				return fmt.Errorf("unknown metrics source: %s (use 'mock' or 'k8s')", metricsStr)
			}

			// 3. Create the LLM engine with rule-based fallback
			llmClient := llm.NewClient(ollamaURL, model)
			engine := decision.NewFallbackEngine(
				llm.NewEngine(llmClient),
				decision.NewRuleBasedEngine(),
			)

			// 4. Start the monitoring loop in background
			go styledMonitorLoop(ctx, provider, engine, st, interval)

			// 5. Start the read-only API server. The dashboard observes metrics
			//    and decisions but never mutates the cluster — execution happens
			//    only through the CLI (analyze --apply, the REPL review flow) or
			//    autonomously via multi-monitor --auto-safe.
			server := api.NewServer(st, port)
			styledLog("Read-only API server listening on %s", ui.CyanStyle.Render(":"+port))
			return server.Start(ctx)
		},
	}

	cmd.Flags().StringVar(&port, "port", "8080", "API server port")
	cmd.Flags().StringVar(&ollamaURL, "ollama-url", "http://localhost:11434", "Ollama server URL")
	cmd.Flags().StringVar(&model, "model", "llama3", "LLM model name")
	cmd.Flags().StringVar(&metricsStr, "metrics", "mock", "Metrics source (mock or k8s)")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace to monitor (empty for all namespaces)")
	cmd.Flags().DurationVar(&interval, "interval", 3*time.Second, "Monitoring loop interval")

	return cmd
}

// styledMonitorLoop collects metrics on interval and hands each snapshot to a
// separate analysis goroutine. Collection is fast, but LLM analysis can take many
// seconds (or stall on a slow Ollama), so running the two on one goroutine would
// let a slow model freeze metrics collection and leave the dashboard's snapshot
// stale. Instead the collection loop keeps ticking and publishing fresh snapshots
// while analysis runs asynchronously against the latest one.
func styledMonitorLoop(ctx context.Context, provider metrics.Provider, engine decision.Engine, st *store.Store, interval time.Duration) {
	styledLog := func(format string, args ...interface{}) {
		ts := ui.DimStyle.Render(time.Now().Format("15:04:05"))
		msg := fmt.Sprintf(format, args...)
		fmt.Printf("  %s  %s\n", ts, msg)
	}

	// analyzeCh is a single-slot, drop-latest hand-off: if analysis is still busy
	// when a fresh snapshot arrives, the waiting one is replaced so we never queue
	// stale work behind a slow LLM call.
	analyzeCh := make(chan *types.ClusterSnapshot, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case snapshot := <-analyzeCh:
				decisions, err := engine.Analyze(snapshot)
				if err != nil {
					styledLog("%s analyzing: %v", ui.RedStyle.Render("ERROR"), err)
					continue
				}

				for _, d := range decisions {
					// Decisions that propose a real action wait for human approval;
					// informational ones (action == none) have nothing to approve.
					if d.Action == types.ActionNone {
						d.Status = types.StatusNone
					} else {
						d.Status = types.StatusPending
					}
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
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	styledLog("Monitor loop started")

	for {
		select {
		case <-ctx.Done():
			styledLog("Monitor loop stopped")
			wg.Wait()
			return
		case <-ticker.C:
			snapshot, err := provider.GetClusterSnapshot()
			if err != nil {
				styledLog("%s collecting metrics: %v", ui.RedStyle.Render("ERROR"), err)
				continue
			}

			st.SetSnapshot(snapshot)

			// Hand off to the analyzer without blocking. If it is still working,
			// replace the pending snapshot with this fresher one (drop-latest).
			select {
			case analyzeCh <- snapshot:
			default:
				select {
				case <-analyzeCh:
				default:
				}
				select {
				case analyzeCh <- snapshot:
				default:
				}
			}
		}
	}
}
