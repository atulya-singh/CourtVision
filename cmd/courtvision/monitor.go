package main

import (
	"fmt"
	"log"
	"time"

	"github.com/spf13/cobra"
	// These would be your actual import paths:
	"github.com/atulya-singh/CourtVision/internal/api"
	"github.com/atulya-singh/CourtVision/internal/decision"
	"github.com/atulya-singh/CourtVision/internal/llm"
	"github.com/atulya-singh/CourtVision/internal/metrics"
	"github.com/atulya-singh/CourtVision/internal/store"
)

func monitorCmd() *cobra.Command {
	// define flag variables - these get filled in when cobra parses the command line

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
			log.SetFlags(log.Ltime | log.Lmsgprefix)
			log.SetPrefix("[CourtVision] ")

			log.Println("Starting Agentic Infrastructure Monitor")
			log.Printf("  Metrics:  %s", metricsStr)
			log.Printf("  Ollama:   %s (model: %s)", ollamaURL, model)
			log.Printf("  API port: %s", port)
			log.Printf("  Interval: %s", interval)
			log.Printf("  Dry run:  %v", dryRun)
			log.Println("---")

			// 1. Create the shared state store
			st := store.New()

			// 2. Choose metrics provider based on flag
			var provider metrics.Provider
			switch metricsStr {
			case "mock":
				log.Println("Using mock metrics provider")
				provider = metrics.NewMockProvider()
			case "k8s":
				log.Println("Using Kubernetes metrics provider")
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
			go monitorLoop(provider, engine, st, interval)

			// 5. Start the API server (blocks forever)
			server := api.NewServer(st, port)
			return server.Start()

		},
	}
	// Register flags on this command
	cmd.Flags().StringVar(&port, "port", "8080", "API server port")
	cmd.Flags().StringVar(&ollamaURL, "ollama-url", "http://localhost:11434", "Ollama server URL")
	cmd.Flags().StringVar(&model, "model", "llama3", "LLM model name")
	cmd.Flags().StringVar(&metricsStr, "metrics", "mock", "Metrics source (mock or k8s)")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace to monitor (empty for all namespaces)")
	cmd.Flags().DurationVar(&interval, "interval", 3*time.Second, "Monitoring loop interval")
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "Log decisions without executing them")

	return cmd
}

func monitorLoop(provider metrics.Provider, engine decision.Engine, st *store.Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Println("Monitor loop started")

	for range ticker.C {
		snapshot, err := provider.GetClusterSnapshot()
		if err != nil {
			log.Printf("ERROR collecting metrics: %v", err)
			continue
		}

		st.SetSnapshot(snapshot)

		decisions, err := engine.Analyze(snapshot)
		if err != nil {
			log.Printf("ERROR analyzing: %v", err)
			continue
		}

		for _, d := range decisions {
			st.AddDecision(d)
			log.Printf("Decision: [%s] %s -> %s", d.Severity, d.TargetPod, d.Action)
		}

		if len(decisions) == 0 {
			log.Println("Cycle complete - no issues")
		}
	}
}
