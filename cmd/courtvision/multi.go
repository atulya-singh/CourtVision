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
	"github.com/atulya-singh/CourtVision/internal/audit"
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
		autoSafe      bool
		autoCooldown  time.Duration
		auditLog      string
		auditMaxBytes int64
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

			if autoSafe {
				configLines = append(configLines, ui.ConfigLine("Auto-safe:", ui.GreenStyle.Render("ON")+ui.DimStyle.Render(fmt.Sprintf(" (reversible actions, cooldown %s)", autoCooldown))))
			} else {
				configLines = append(configLines, ui.ConfigLine("Auto-safe:", ui.DimStyle.Render("off")))
			}

			// Open the audit trail once and share it across every worker. The
			// in-memory ring always exists (so /api/audit works); a file is added
			// when --audit-log is set. auditReader backs the read-only API.
			sink, auditReader, auditLabel, err := buildAuditSink(auditLog, auditMaxBytes)
			if err != nil {
				return err
			}
			defer sink.Close()
			configLines = append(configLines, ui.ConfigLine("Audit log:", auditLabel))

			fmt.Println(ui.ConfigBox.Render(strings.Join(configLines, "\n")))
			fmt.Println()

			// Auto-safe + LIVE means the agents mutate real clusters with no human
			// in the loop — make that impossible to miss.
			if autoSafe && !dryRun {
				fmt.Println(ui.RedStyle.Render("  ⚠  auto-safe + LIVE: workers will autonomously execute reversible actions on real clusters"))
				fmt.Println()
			}

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

				exec, err := buildClusterExecutor(metricsStr, dryRun, name, sink)
				if err != nil {
					return fmt.Errorf("cluster %q: %w", name, err)
				}

				workers = append(workers, cluster.NewClusterWorker(name, provider, engine, exec, autoSafe, autoCooldown, sink))
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
			server := api.NewMultiServer(workers, masterStore, auditReader, port)
			styledLog("Read-only API server listening on %s", ui.CyanStyle.Render(":"+port))
			styledLog("%s", ui.DimStyle.Render("Dashboard is view-only; execution is auto-safe or CLI-driven"))
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
	cmd.Flags().BoolVar(&autoSafe, "auto-safe", false, "Let each worker auto-execute its own reversible decisions (cordon_node, scale_down, patch_limits); evict_and_move still waits for approval")
	cmd.Flags().DurationVar(&autoCooldown, "auto-cooldown", 3*time.Minute, "In auto-safe mode, suppress repeat auto-execution of the same action on the same target for this long")
	cmd.Flags().StringVar(&auditLog, "audit-log", "", "Append a durable JSONL record of every executed action (all clusters) to this file (empty = disabled)")
	cmd.Flags().Int64Var(&auditMaxBytes, "audit-max-bytes", 0, "Rotate the audit log when it exceeds this many bytes, keeping a few numbered backups (0 = never rotate)")

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
// to its kubeconfig context, and anything else gets a simulated one. The result
// is wrapped in an audit decorator stamped with this cluster's name, so every
// action a worker takes — whether auto-safe or operator-approved — is recorded
// against the right cluster in the shared audit log.
func buildClusterExecutor(source string, dryRun bool, contextName string, sink audit.Sink) (executor.Executor, error) {
	var (
		base executor.Executor
		mode string
	)
	switch {
	case dryRun:
		base, mode = executor.NewDryRunExecutor(), "dry-run"
	case source == "k8s":
		e, err := executor.NewK8sExecutor(contextName)
		if err != nil {
			return nil, err
		}
		base, mode = e, "live"
	default:
		base, mode = executor.NewMockExecutor(), "mock"
	}
	return audit.NewExecutor(base, sink, contextName, mode, dryRun), nil
}
