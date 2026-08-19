# CourtVision

An autonomous Kubernetes controller that uses a local LLM to analyze real-time cluster metrics and make intelligent infrastructure decisions. Instead of blindly restarting failing pods, CourtVision reasons about resource contention, noisy neighbor problems, and capacity constraints — then recommends whether to adjust resource limits, migrate pods to different nodes, or scale deployments.

CourtVision runs in two shapes. **Single-cluster mode** watches one cluster with one agent. **Multi-agent mode** runs one subagent per cluster, each monitoring its own cluster in parallel, plus a master coordinator that reasons across the whole fleet — spotting cross-cluster opportunities like relieving an overloaded cluster by shifting work to one with spare capacity.

## How It Works

CourtVision runs a continuous monitoring loop that collects resource metrics from your Kubernetes cluster every few seconds, feeds them to a local LLM (Llama 3 via Ollama), and surfaces structured decisions through a REST API and real-time dashboard.

```
┌─────────────┐     ┌──────────────┐     ┌───────────┐     ┌───────────────┐
│  Kubernetes  │────▶│  CourtVision │────▶│  Ollama   │────▶│   Decisions   │
│  Cluster     │     │  Agent       │     │  (LLM)    │     │   + Dashboard │
│              │◀────│              │◀────│           │     │               │
│  metrics-    │     │  - collect   │     │  Llama 3  │     │  - REST API   │
│  server      │     │  - analyze   │     │  local    │     │  - SSE stream │
│              │     │  - decide    │     │           │     │  - React UI   │
└─────────────┘     └──────────────┘     └───────────┘     └───────────────┘
```

The LLM doesn't just detect problems — it explains its reasoning in natural language and chooses the optimal remediation action from a set of available operations:

- **patch_limits** — adjust CPU/memory limits when a pod is near capacity but the node has headroom
- **evict_and_move** — migrate a pod to a less-loaded node when the current node is under pressure
- **scale_down** — reduce replicas when a deployment is over-provisioned
- **none** — continue monitoring when metrics are elevated but not dangerous

### Multi-Agent Mode

For fleets with more than one cluster, `courtvision multi-monitor` runs a two-tier topology:

```
                         ┌──────────────────────────┐
                         │   Coordinator (master)   │   slow loop (~30s)
                         │   reads cached snapshots  │   cross-cluster reasoning
                         │   → fleet-wide decisions  │   → /api/decisions
                         └────────────┬─────────────┘
              reads LatestSnapshot()  │  (never touches clusters directly)
            ┌────────────────────────┼────────────────────────┐
            ▼                        ▼                         ▼
   ┌─────────────────┐     ┌─────────────────┐      ┌─────────────────┐
   │ ClusterWorker   │     │ ClusterWorker   │      │ ClusterWorker   │  fast loop (~5s)
   │   prod-us       │     │   prod-eu       │      │   staging       │  collect→analyze→store
   │ own store+exec  │     │ own store+exec  │      │ own store+exec  │  /api/clusters/{name}/…
   └────────┬────────┘     └────────┬────────┘      └────────┬────────┘
            ▼                        ▼                         ▼
        cluster A                cluster B                 cluster C
```

- **Subagents (`ClusterWorker`)** — one per cluster, each running the full collect → analyze → store pipeline on its own fast loop. Each owns its own decision store and an executor bound to that cluster's kubeconfig context, so any auto-safe action lands on the right cluster.
- **Master agent (`Coordinator`)** — runs a deliberately slower loop. It reads each worker's *cached* snapshot (it never collects from clusters itself), asks the LLM to reason about the fleet as a whole, and records cross-cluster decisions in its own store. It only runs once at least two clusters have reported, so cold start and single-cluster cases stay quiet.
- **Cluster attribution** — coordinator decisions carry a `target_cluster` so the fleet view can attribute each cross-cluster recommendation to the cluster it targets. These decisions are **surfaced read-only** (advisory); the HTTP API never executes them — auto-safe is per-cluster and reversible-only, and cross-cluster moves stay operator-driven.

The two loops are independently tunable (`--interval` for workers, `--coordinator-interval` for the master). The master is meant to run several times slower than the workers — it reads cached state, so running it faster buys nothing, and cross-cluster moves are strategic decisions that shouldn't be re-litigated every few seconds.

## Features

- **Real Kubernetes integration** — connects to any cluster via kubeconfig (AWS EKS, GKE, AKS, Minikube, Kind)
- **Multi-cluster, multi-agent** — one subagent per cluster plus a master coordinator for cross-cluster reasoning, all in a single process
- **Local LLM analysis** — uses Ollama with Llama 3 for on-device inference, no data leaves your machine
- **Interactive CLI** — styled REPL with a `/` command palette (filter, arrow-navigate, Tab-complete) and switchable session modes, plus colored output and spinners
- **Real-time dashboard** — React frontend with glassmorphism UI, live metric visualization, and SSE-powered decision feed
- **Mock mode** — full demo experience without a cluster or LLM, using simulated metrics with a noisy neighbor scenario
- **Dry-run by default** — decisions are proposed and displayed but never executed unless explicitly enabled
- **Rule-based fallback** — deterministic engine catches critical issues even if the LLM is unavailable

## Installation

### From source (requires Go 1.22+)

```bash
git clone https://github.com/atulya-singh/CourtVision.git
cd CourtVision
go build -o courtvision ./cmd/courtvision/
```

### Go install

```bash
go install github.com/atulya-singh/CourtVision/cmd/courtvision@latest
```

### Prerequisites

- **Ollama** — install from [ollama.com](https://ollama.com), then run `ollama pull llama3`
- **Kubernetes cluster** (optional) — any cluster with metrics-server installed. For local testing, use [Kind](https://kind.sigs.k8s.io/):

```bash
kind create cluster
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
kubectl patch deployment metrics-server -n kube-system --type='json' \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
```

## Quickstart

### Interactive mode

```bash
courtvision
```

This drops you into the CourtVision REPL. Press **`/`** to open a command
palette — a live-filtered, arrow-navigable menu you drive with
almost no typing — and switch **session modes** once instead of re-typing flags:

```
◈ CourtVision v1.0.0

  ● ollama · metrics:mock · ns:all · sandbox
› /                      ← press "/" to open the palette
  ▸ /analyze             Run a one-shot cluster analysis
    /review              Analyze, then approve/reject each action inline
    /metrics <mock|k8s>  Switch the metrics source for this session
    /namespace <ns|all>  Set the namespace filter for this session
    …                    (↑/↓ move · Tab complete · Enter run · Esc dismiss)
```

- **Palette** — type `/` then filter (`/an` → `/analyze`); `↑/↓` highlight,
  `Tab` completes, `Enter` runs, `Esc` dismisses. `↑/↓` still cycle command
  history when the palette is closed.
- **Modes** — `/metrics k8s`, `/namespace kube-system`, or **`Shift+Tab`** to
  toggle mock↔k8s. The mode bar above the prompt always shows the current context,
  and `/review` / `/analyze` run under it (no repeated `--metrics`/`--namespace`).
- **Back-compat** — bare words (`review`, `status`, `help`, `exit`) still work.
  Long-running servers (`/monitor`, `/multi-monitor`) print the shell command to
  run rather than taking over the REPL. The REPL `review` flow is a **sandbox**
  (k8s falls back to dry-run), so switching `/metrics k8s` never mutates a cluster.

```
› /review
  Analyzing cluster...
  Review 1/2  [critical]
  Pod:    default/data-pipeline-545bf66bc6
  Action: patch_limits
  Why:    Pod consuming 100% of CPU limit, recommend raising the ceiling
  [a]pprove  [r]eject  [s]kip  [A]pprove all  [tab] auto: off  [q]uit

› exit
```

### One-shot commands

```bash
# Check that Ollama and Kubernetes are reachable
courtvision status

# Quick cluster analysis — print results and exit
courtvision analyze --metrics k8s --namespace production --output table

# Same analysis as JSON (for piping to jq or other tools)
courtvision analyze --metrics k8s --output json

# Start continuous monitoring with dashboard
courtvision monitor --metrics k8s --namespace production --port 8080

# Run with mock data (no cluster needed)
courtvision monitor --metrics mock --port 8080

# Multi-agent mode — one subagent per cluster + a cross-cluster coordinator
courtvision multi-monitor --clusters prod-us,prod-eu,staging --metrics k8s --interval 5s

# Multi-agent mode with mock data (two simulated clusters, no cluster or LLM needed)
courtvision multi-monitor --clusters mock-a,mock-b --metrics mock
```

In multi-agent mode the API exposes per-cluster state and the coordinator's fleet-wide decisions:

```bash
curl localhost:8080/api/clusters                       # fleet roll-up (pods/nodes/decisions per cluster)
curl localhost:8080/api/clusters/prod-us/snapshot      # one cluster's latest snapshot
curl localhost:8080/api/clusters/prod-us/decisions     # one cluster's decisions
curl localhost:8080/api/decisions                       # coordinator's cross-cluster decisions
```

### Dashboard

When the monitor is running, open `http://localhost:8080` for the API, or start the React dashboard:

```bash
cd web
npm install
npm run dev
```

Then open `http://localhost:5173` to see the real-time dashboard with cluster visualization and streaming LLM decisions.

## CLI Reference

### `courtvision monitor`

Start the continuous monitoring agent with a **read-only** API server and
dashboard. The dashboard observes metrics and decisions but never mutates the
cluster; to execute a decision use `analyze --apply`, the REPL `review` flow, or
`multi-monitor --auto-safe`.

| Flag | Default | Description |
|------|---------|-------------|
| `--metrics` | `mock` | Metrics source: `mock` or `k8s` |
| `--namespace` | `` (all) | Kubernetes namespace to watch |
| `--port` | `8080` | API server port |
| `--ollama-url` | `http://localhost:11434` | Ollama server URL |
| `--model` | `llama3` | LLM model name |
| `--interval` | `3s` | Monitoring loop interval |

### `courtvision multi-monitor`

Monitor multiple clusters with one subagent each plus a cross-cluster coordinator.

| Flag | Default | Description |
|------|---------|-------------|
| `--clusters` | (required) | Comma-separated kubeconfig context names to monitor |
| `--metrics` | `mock` | Metrics source: `mock` or `k8s` |
| `--namespace` | `` (all) | Kubernetes namespace to watch (applies to every cluster) |
| `--port` | `8080` | API server port |
| `--ollama-url` | `http://localhost:11434` | Ollama server URL |
| `--model` | `llama3` | LLM model name |
| `--interval` | `5s` | Per-cluster worker loop interval |
| `--coordinator-interval` | `30s` | Coordinator (cross-cluster) loop interval |
| `--dry-run` | `true` | Log decisions without executing |
| `--auto-safe` | `false` | Let each worker auto-execute its own **reversible** decisions (`cordon_node`, `scale_down`, `patch_limits`); `evict_and_move` still waits for approval |
| `--auto-cooldown` | `3m` | In auto-safe mode, suppress repeat auto-execution of the same action on the same target for this long |
| `--audit-log` | `` (off) | Append a durable JSONL record of every executed action (all clusters) to this file |
| `--audit-max-bytes` | `0` (unbounded) | Rotate the audit log past this size, keeping a few numbered backups |

> The audit trail is also served read-only at `GET /api/audit` (fleet) and `GET /api/clusters/{cluster}/audit` (per-cluster), newest-first — live even without `--audit-log` (backed by an in-memory ring).

> **Auto-safe** is the unattended tier: each worker heals its own cluster's reversible issues without a human in the loop, while the coordinator's cross-cluster moves stay advisory (surfaced read-only, no auto-execution). `--dry-run` is still the separate safety switch — `--auto-safe` with `--dry-run=false` performs **real autonomous mutations** on live clusters (the startup banner warns when both are set). The per-target cooldown stops the ~5s analysis loop from re-firing the same fix every tick.

### `courtvision analyze`

Run a one-shot cluster analysis and exit.

| Flag | Default | Description |
|------|---------|-------------|
| `--metrics` | `mock` | Metrics source: `mock` or `k8s` |
| `--namespace` | `` (all) | Kubernetes namespace to watch |
| `--output` | `table` | Output format: `table` or `json` |
| `--ollama-url` | `http://localhost:11434` | Ollama server URL |
| `--model` | `llama3` | LLM model name |

### `courtvision audit-verify <file>`

Verify the tamper-evidence hash chain of a JSONL audit log written with `--audit-log`. Reports the first event whose content was altered or whose link was broken (edited, reordered, or removed), or confirms the chain is intact. Exits non-zero on tampering, so it drops into a cron/CI check. Verify a single unrotated file — each rotated segment (`<file>.1`, `.2`, …) carries its own independent chain.

### `courtvision status`

Check connectivity to Ollama and Kubernetes.

### `courtvision version`

Print version, commit hash, and build date.

## Architecture

```
CourtVision/
├── cmd/courtvision/          ← CLI entry point (Cobra commands)
│   ├── main.go               ← root command + REPL
│   ├── monitor.go             ← continuous (single-cluster) monitoring subcommand
│   ├── multi.go               ← multi-cluster monitoring subcommand (workers + coordinator)
│   ├── analyze.go             ← one-shot analysis subcommand
│   └── status.go              ← connectivity check subcommand
├── internal/
│   ├── types/                 ← shared data structures (PodMetrics, Decision, etc.)
│   ├── metrics/
│   │   ├── mock.go            ← simulated cluster with noisy neighbor
│   │   └── k8s.go             ← real Kubernetes metrics via client-go (kubeconfig context aware)
│   ├── llm/
│   │   ├── client.go          ← Ollama HTTP client
│   │   ├── engine.go          ← LLM decision engine (implements Engine interface)
│   │   ├── prompt.go          ← cluster snapshot → structured LLM prompt (+ cross-cluster prompt)
│   │   └── parser.go          ← LLM text output → structured decisions
│   ├── decision/
│   │   └── engine.go          ← rule-based fallback engine
│   ├── cluster/
│   │   ├── worker.go          ← ClusterWorker: per-cluster subagent pipeline
│   │   └── coordinator.go     ← Coordinator: master agent for cross-cluster reasoning
│   ├── store/
│   │   └── store.go           ← thread-safe shared state with SSE pub/sub
│   ├── api/
│   │   └── server.go          ← REST API + SSE endpoints (single- and multi-cluster routes)
│   └── ui/
│       └── styles.go          ← terminal styling (lipgloss colors, layouts)
└── web/                       ← React dashboard (Vite + TypeScript + Tailwind)
```

### Data Flow

1. **Metrics Provider** (`mock.go` or `k8s.go`) collects a cluster snapshot every N seconds, stamped with the cluster's name
2. **LLM Engine** converts the snapshot to a prompt, sends it to Ollama, parses the response into structured decisions
3. **Store** saves the snapshot and decisions, notifies SSE subscribers
4. **API Server** serves cluster state via REST and streams decisions via SSE — **read-only**; it never mutates the cluster
5. **Dashboard** (React) renders the cluster visually and shows the decision feed in real-time

In multi-agent mode this pipeline runs once per cluster inside a `ClusterWorker`, each with its own store and executor. A `Coordinator` sits on top: it periodically reads every worker's cached snapshot, builds a single cross-cluster prompt, and records fleet-wide decisions in its own store. Because cluster identity flows through `ClusterSnapshot` and `Decision`, decisions are always attributable to the cluster they belong to; execution happens only through the CLI or a worker's own auto-safe loop, never through the API.

### Key Design Pattern

Every major component is behind an interface — `metrics.Provider`, `decision.Engine`, `llm.Generatable`. This means you can swap implementations without changing any other code. Mock metrics → real Kubernetes. Rule engine → LLM engine. Local Ollama → remote API. One line change in the wiring, zero changes elsewhere.

This is exactly what makes multi-agent mode cheap: a `ClusterWorker` is just the single-cluster pipeline behind those same interfaces, parameterized by a kubeconfig context. Spinning up N clusters is N instances of the same wiring, and the `Coordinator` composes them without any of them knowing it exists.

## Tech Stack

- **Go** — agent, CLI, API server
- **client-go** — Kubernetes API client
- **Ollama + Llama 3** — local LLM inference
- **Cobra** — CLI framework
- **Lipgloss / Bubbletea** — terminal UI styling
- **React + TypeScript + Tailwind** — dashboard frontend
- **Server-Sent Events** — real-time streaming

