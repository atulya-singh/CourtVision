# CourtVision

An autonomous Kubernetes controller that uses a local LLM to analyze real-time cluster metrics and make intelligent infrastructure decisions. Instead of blindly restarting failing pods, CourtVision reasons about resource contention, noisy neighbor problems, and capacity constraints — then recommends whether to adjust resource limits, migrate pods to different nodes, or scale deployments.

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

## Features

- **Real Kubernetes integration** — connects to any cluster via kubeconfig (AWS EKS, GKE, AKS, Minikube, Kind)
- **Local LLM analysis** — uses Ollama with Llama 3 for on-device inference, no data leaves your machine
- **Interactive CLI** — styled terminal interface with REPL mode, colored output, and spinners
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

This drops you into the CourtVision REPL where you can type commands directly:

```
◈ CourtVision v1.0.0

› status
  Ollama:     ✓ Connected (http://localhost:11434)
  Models:     llama3:latest
  Kubernetes: ✓ Connected (kind-kind)

› analyze --metrics k8s --namespace default --output table
  Analyzing cluster... ⠋

  SEVERITY   POD                          ACTION          REASONING
  ──────────  ─────────────────────────────  ───────────────  ──────────────────────────────────────────
  critical   data-pipeline-545bf66bc6     patch_limits    Pod consuming 100% of CPU limit, recomm...
  medium     worker-queue-5f8d            none            Memory elevated at 78%, monitoring...

  Found 2 issues in default (analyzed in 3.2s)

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

Start the continuous monitoring agent with API server.

| Flag | Default | Description |
|------|---------|-------------|
| `--metrics` | `mock` | Metrics source: `mock` or `k8s` |
| `--namespace` | `` (all) | Kubernetes namespace to watch |
| `--port` | `8080` | API server port |
| `--ollama-url` | `http://localhost:11434` | Ollama server URL |
| `--model` | `llama3` | LLM model name |
| `--interval` | `3s` | Monitoring loop interval |
| `--dry-run` | `true` | Log decisions without executing |

### `courtvision analyze`

Run a one-shot cluster analysis and exit.

| Flag | Default | Description |
|------|---------|-------------|
| `--metrics` | `mock` | Metrics source: `mock` or `k8s` |
| `--namespace` | `` (all) | Kubernetes namespace to watch |
| `--output` | `table` | Output format: `table` or `json` |
| `--ollama-url` | `http://localhost:11434` | Ollama server URL |
| `--model` | `llama3` | LLM model name |

### `courtvision status`

Check connectivity to Ollama and Kubernetes.

### `courtvision version`

Print version, commit hash, and build date.

## Architecture

```
CourtVision/
├── cmd/courtvision/          ← CLI entry point (Cobra commands)
│   ├── main.go               ← root command + REPL
│   ├── monitor.go             ← continuous monitoring subcommand
│   ├── analyze.go             ← one-shot analysis subcommand
│   └── status.go              ← connectivity check subcommand
├── internal/
│   ├── types/                 ← shared data structures (PodMetrics, Decision, etc.)
│   ├── metrics/
│   │   ├── mock.go            ← simulated cluster with noisy neighbor
│   │   └── k8s.go             ← real Kubernetes metrics via client-go
│   ├── llm/
│   │   ├── client.go          ← Ollama HTTP client
│   │   ├── engine.go          ← LLM decision engine (implements Engine interface)
│   │   ├── prompt.go          ← cluster snapshot → structured LLM prompt
│   │   └── parser.go          ← LLM text output → structured decisions
│   ├── decision/
│   │   └── engine.go          ← rule-based fallback engine
│   ├── store/
│   │   └── store.go           ← thread-safe shared state with SSE pub/sub
│   ├── api/
│   │   └── server.go          ← REST API + SSE streaming endpoints
│   └── ui/
│       └── styles.go          ← terminal styling (lipgloss colors, layouts)
└── web/                       ← React dashboard (Vite + TypeScript + Tailwind)
```

### Data Flow

1. **Metrics Provider** (`mock.go` or `k8s.go`) collects a cluster snapshot every N seconds
2. **LLM Engine** converts the snapshot to a prompt, sends it to Ollama, parses the response into structured decisions
3. **Store** saves the snapshot and decisions, notifies SSE subscribers
4. **API Server** serves cluster state via REST and streams decisions via SSE
5. **Dashboard** (React) renders the cluster visually and shows the decision feed in real-time

### Key Design Pattern

Every major component is behind an interface — `metrics.Provider`, `decision.Engine`, `llm.Generatable`. This means you can swap implementations without changing any other code. Mock metrics → real Kubernetes. Rule engine → LLM engine. Local Ollama → remote API. One line change in the wiring, zero changes elsewhere.

## Tech Stack

- **Go** — agent, CLI, API server
- **client-go** — Kubernetes API client
- **Ollama + Llama 3** — local LLM inference
- **Cobra** — CLI framework
- **Lipgloss / Bubbletea** — terminal UI styling
- **React + TypeScript + Tailwind** — dashboard frontend
- **Server-Sent Events** — real-time streaming

## License

MIT
