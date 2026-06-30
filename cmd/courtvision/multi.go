package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/atulya-singh/CourtVision/internal/api"
	"github.com/atulya-singh/CourtVision/internal/cluster"
	"github.com/atulya-singh/CourtVision/internal/decision"
	"github.com/atulya-singh/CourtVision/internal/executor"
	"github.com/atulya-singh/CourtVision/internal/llm"
	"github.com/atulya-singh/CourtVision/internal/metrics"
	"github.com/atulya-singh/CourtVision/internal/store"
	"github.com/atulya-singh/CourtVision/internal/ui"
	"github.com/spf13/cobra"
)

func multiMonitorCmd() *cobra.Command {
	var (
		clustersStr   string
		port          string
		ollamaURL     string
		model         string
		metricsStr    string
		namespace     string
		interval      time.Duration
		coordInterval time.Duration
		dryRun        bool
	)

	cmd := &cobra.Command{
		Use:   "multi-monitor",
		Short: "Monitor multiple clusters with per-cluster agents and a coordinator",
		Long: `Start CourtVision in multi-agent mode. One subagent (ClusterWorker)
monitors each cluster on its own fast loop, while a master Coordinator runs a
slower loop that reasons across all clusters at once — spotting cross-cluster
opportunities like relieving an overloaded cluster by shifting work to one with
spare capacity.

Each cluster is a kubeconfig context name passed via --clusters. The API serves
per-cluster state under /api/clusters/{cluster}/... and the coordinator's
cross-cluster decisions under /api/decisions.`,

		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			clusters := splitClusters(clustersStr)
			if len(clusters) == 0 {
				return fmt.Errorf("--clusters is required (comma-separated kubeconfig context names)")
			}

			// Print banner
			fmt.Println(ui.Banner())
			fmt.Println()

			// Build config box
			var configLines []string
			configLines = append(configLines, ui.BrandStyle.Render("Configuration"))
			configLines = append(configLines, "")
			configLines = append(configLines, ui.ConfigLine("Clusters:", strings.Join(clusters, ", ")))
			configLines = append(configLines, ui.ConfigLine("Metrics:", metricsStr))
			configLines = append(configLines, ui.ConfigLine("Ollama:", fmt.Sprintf("%s (model: %s)", ollamaURL, ui.CyanStyle.Render(model))))
			configLines = append(configLines, ui.ConfigLine("API port:", port))
			configLines = append(configLines, ui.ConfigLine("Worker interval:", interval.String()))
			configLines = append(configLines, ui.ConfigLine("Coordinator:", coordInterval.String()))

			if dryRun {
				configLines = append(configLines, ui.ConfigLine("Mode:", ui.DryRunBadge))
			} else {
				configLines = append(configLines, ui.ConfigLine("Mode:", ui.GreenStyle.Render("LIVE")))
			}

			fmt.Println(ui.ConfigBox.Render(strings.Join(configLines, "\n")))
			fmt.Println()

			log.SetFlags(0)
			log.SetPrefix("")

			styledLog := func(format string, args ...interface{}) {
				ts := ui.DimStyle.Render(time.Now().Format("15:04:05"))
				msg := fmt.Sprintf(format, args...)
				fmt.Printf("  %s  %s\n", ts, msg)
			}

			styledLog("Starting multi-cluster monitor across %d cluster(s)", len(clusters))
			styledLog("%s", ui.DimStyle.Render("───────────────────────────────────────"))

			// The Ollama client is shared by every worker and the coordinator.
			llmClient := llm.NewClient(ollamaURL, model)

			// Build one subagent per cluster.
			workers := make([]*cluster.ClusterWorker, 0, len(clusters))
			for _, name := range clusters {
				provider, err := buildClusterProvider(metricsStr, namespace, name)
				if err != nil {
					return fmt.Errorf("cluster %q: %w", name, err)
				}

				engine := decision.NewFallbackEngine(
					llm.NewEngine(llmClient),
					decision.NewRuleBasedEngine(),
				)

				exec, err := buildClusterExecutor(metricsStr, dryRun, name)
				if err != nil {
					return fmt.Errorf("cluster %q: %w", name, err)
				}

				workers = append(workers, cluster.NewClusterWorker(name, provider, engine, exec))
				styledLog("Worker ready for cluster %s", ui.CyanStyle.Render(name))
			}

			// The coordinator owns its own store for cross-cluster decisions.
			masterStore := store.New()
			coord := cluster.NewCoordinator(workers, llmClient, masterStore, coordInterval)

			// Start every subagent and the coordinator.
			for _, w := range workers {
				go w.Run(ctx, interval)
			}
			go coord.Run(ctx)

			// Serve per-cluster and fleet-level state until ctx is cancelled.
			server := api.NewMultiServer(workers, masterStore, port)
			styledLog("API server listening on %s", ui.CyanStyle.Render(":"+port))
			return server.Start(ctx)
		},
	}

	cmd.Flags().StringVar(&clustersStr, "clusters", "", "Comma-separated kubeconfig context names to monitor (required)")
	cmd.Flags().StringVar(&port, "port", "8080", "API server port")
	cmd.Flags().StringVar(&ollamaURL, "ollama-url", "http://localhost:11434", "Ollama server URL")
	cmd.Flags().StringVar(&model, "model", "llama3", "LLM model name")
	cmd.Flags().StringVar(&metricsStr, "metrics", "mock", "Metrics source (mock or k8s)")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace to monitor (empty for all namespaces)")
	cmd.Flags().DurationVar(&interval, "interval", 5*time.Second, "Per-cluster worker loop interval")
	cmd.Flags().DurationVar(&coordInterval, "coordinator-interval", 30*time.Second, "Coordinator (cross-cluster) loop interval")
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "Log decisions without executing them")

	return cmd
}

// splitClusters parses the comma-separated --clusters flag, trimming whitespace
// and dropping empty entries.
func splitClusters(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if name := strings.TrimSpace(part); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// buildClusterProvider builds a metrics provider scoped to one cluster. The mock
// source stamps the cluster name; the k8s source targets that kubeconfig context.
func buildClusterProvider(source, namespace, contextName string) (metrics.Provider, error) {
	switch source {
	case "mock":
		return metrics.NewMockProvider(contextName), nil
	case "k8s":
		return metrics.NewK8sProvider(namespace, contextName)
	default:
		return nil, fmt.Errorf("unknown metrics source: %s (use 'mock' or 'k8s')", source)
	}
}

// buildClusterExecutor mirrors buildExecutor's safety switch but targets a
// specific cluster: --dry-run always wins, a real cluster gets an executor bound
// to its kubeconfig context, and anything else gets a simulated one.
func buildClusterExecutor(source string, dryRun bool, contextName string) (executor.Executor, error) {
	switch {
	case dryRun:
		return executor.NewDryRunExecutor(), nil
	case source == "k8s":
		return executor.NewK8sExecutor(contextName)
	default:
		return executor.NewMockExecutor(), nil
	}
}
