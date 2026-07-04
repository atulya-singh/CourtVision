# CLAUDE.md — CourtVision

Context for future sessions. CourtVision is an **agentic Kubernetes controller**:
it collects cluster metrics, asks a local LLM (Ollama/Llama 3) what to do, and —
only after approval — executes narrowly-scoped remediations. Go backend + a
React/TS dashboard.

- Module: `github.com/atulya-singh/CourtVision` · Go 1.26
- Binary: `courtvision` (built from `cmd/courtvision`)

---

## Commit & workflow conventions (IMPORTANT)

- **Do NOT add a `Co-Authored-By` trailer to commits.** No `Co-Authored-By: Claude ...`
  line, no "Generated with Claude Code" footer. Plain commit messages only.
- Commit message style used throughout the history: a concise imperative
  **subject line**, a blank line, then a body explaining **why** + **what**, and a
  short bullet list of the files/areas touched. End with a one-line note on how it
  was verified when relevant. Example shape:
  ```
  Add --auto-safe unattended mode to multi-monitor

  <why this change / what problem it solves — a paragraph or two>

  - internal/cluster/worker.go: <what changed>
  - cmd/courtvision/multi.go: <what changed>

  Verified live with 3 mock clusters: ...
  ```
- **Only commit/push when the user explicitly asks.** The user typically says
  "commit and push" per step. Push to `main` (this repo commits directly to main).
- Before committing: `go build ./...`, `go vet ./...`, `go test ./...` should all
  be green. Add `-race` on packages with goroutines (`cluster`, `api`, `store`).
- Keep new code in the surrounding style: interface-driven, heavily commented on
  *why* (not what), small pure helpers that are unit-testable.

---

## What the project can do (capabilities)

**Two run modes:**
1. **Single-cluster** (`monitor`) — one pipeline: collect → analyze → serve
   decisions over an HTTP API + SSE dashboard.
2. **Multi-cluster, multi-agent** (`multi-monitor`) — one `ClusterWorker`
   subagent per cluster (each runs the full pipeline against its own kubeconfig
   context) plus one master `Coordinator` that reasons *across* clusters (e.g.
   relocate a pod from an overloaded cluster to one with spare capacity).

**The four executor actions** (the only real mutations the agent can make):
| Action | Effect | Reversible? |
|---|---|---|
| `patch_limits` | Rewrite CPU/mem limits on the owning Deployment, distributed **proportionally across all containers** (sum = pod-level target) | yes (restart) |
| `scale_down` | Reduce Deployment replicas by 1, hard floor of 1 | yes |
| `cordon_node` | Mark a node unschedulable | yes |
| `evict_and_move` | **Delete** the pod (controller reschedules); `target_node` not yet honored | **no** |

**Decision lifecycle:** `none` (informational) / `pending` → `executing` →
`executed` | `failed` | `rejected`.

**Safety model (layered):**
- Nothing mutates a cluster until an `Executor` runs a decision.
- **`--dry-run` (default ON)** = the safety switch: `DryRunExecutor` logs "would…"
  and changes nothing. `mock` metrics → `MockExecutor`. Only `k8s` + LIVE →
  real `K8sExecutor`.
- **Approval gates:**
  - HTTP API (`POST /api/.../decisions/{id}/approve`) — one at a time; multi-cluster
    routes execution by `decision.ClusterName` via `executorFor`.
  - Interactive review (`analyze --apply`, REPL `review`) — `a`/`r`/`s` per item,
    `A` = approve-all, **Tab = sticky auto-accept** (reversible actions only;
    pauses on `evict_and_move`; turns off on failure).
- **`--auto-safe` (multi-monitor, opt-in)** — each worker auto-executes its own
  **reversible** decisions unattended; `evict_and_move` stays pending; a
  **per-target cooldown** (`--auto-cooldown`, default 3m) stops the ~5s loop from
  re-firing the same fix. Coordinator decisions stay human-approved.

**Reversible gate:** `types.ActionType.IsReversible()` is the single source of
truth (cordon/scale_down/patch_limits = true). Used by both the interactive
auto-accept and worker auto-safe.

**Audit trail:** `--audit-log <file>` (opt-in, off by default) appends a durable
JSONL record of every *execution* — `executing` → `executed`/`failed` — to a
file, tagged with the actor (`api-approval`/`auto-safe`/`interactive-review`),
cluster, action, target, reasoning, mode, duration, and error. Implemented as an
`AuditingExecutor` decorator wrapping the safety-switch executor, so it's a
single chokepoint over all paths. Executions only for now (not propose/approve/
reject); see `internal/audit/`.

---

## File map

### `cmd/courtvision/` — CLI (Cobra) + terminal UIs (package `main`)
- `main.go` — root command; registers subcommands; launches REPL when run with no args.
- `monitor.go` — `monitor` subcommand: single-cluster loop + API server. `styledMonitorLoop` collects on interval and analyzes on a **separate goroutine** (non-blocking).
- `multi.go` — `multi-monitor` subcommand: builds one worker per `--clusters` context + a coordinator; flags incl. `--auto-safe` / `--auto-cooldown`; `buildClusterProvider` / `buildClusterExecutor`.
- `analyze.go` — `analyze` one-shot: spinner model, prints table/JSON; `--apply` hands decisions to the interactive review.
- `approve.go` — shared review plumbing: `reviewSession` (pure queue), `buildExecutor` safety switch, `runExecutor`, renderers, `isReversible` (delegates to `types`).
- `apply.go` — `applyModel`: standalone Bubbletea review screen for `analyze --apply`. Has the Tab auto-accept toggle + `autoAdvance`. **Can run LIVE.**
- `repl.go` — interactive REPL; `review` runs the inline approval flow (sandboxed: mock→mock, k8s→dry-run). Mirrors `apply.go`'s auto-accept.
- `status.go` — `status` subcommand (Ollama/K8s connectivity check).
- `*_test.go` — review-flow + auto-accept Update-loop tests.

### `internal/types/` — shared data model
- `types.go` — `Decision`, `ClusterSnapshot`, `PodMetrics`, `NodeMetrics`, action/severity/status enums, `ActionType.IsReversible()`. **Note:** JSON tag casing is intentionally mixed (`Decision.ClusterName` = `"ClusterName"`, `ClusterSnapshot.ClusterName` = `"clusterName"`) — do not "fix".

### `internal/metrics/` — metrics providers (`Provider` interface)
- `k8s.go` — `K8sProvider`: real metrics via client-go + metrics-server; `NewK8sProvider(namespace, contextName)` targets a kubeconfig context. Pod limits/usage are **summed across containers**.
- `mock.go` — `MockProvider`: deterministic fake snapshots; `NewMockProvider(clusterName)`. No cluster/LLM needed.

### `internal/llm/` — LLM analysis
- `client.go` — Ollama HTTP client; 60s timeout, retry + backoff.
- `engine.go` — `Engine.Analyze`: build prompt → generate → parse; stamps `ClusterName` from the snapshot. `Generatable` interface.
- `prompt.go` — `BuildPrompt` (single) + `BuildMultiClusterPrompt` (coordinator, cross-cluster with `target_cluster`); shared `writeClusterMetrics`.
- `parser.go` — parse LLM JSON lines → `[]Decision`; atomic parse counter for concurrent workers.

### `internal/decision/` — non-LLM engines (`Engine` interface)
- `engine.go` — `RuleBasedEngine`: threshold rules (used as fallback / deterministic path).
- `fallback.go` — `FallbackEngine`: try primary (LLM), fall back to secondary (rules) on error. This is what every entry point wires up.

### `internal/executor/` — the only place real mutations happen (`Executor` interface)
- `executor.go` — `MockExecutor` (simulated) + `DryRunExecutor` (logs, no-op).
- `k8s.go` — `K8sExecutor`: real mutations. `patchLimits` distributes the pod-level target across containers proportionally (`distribute` helper); `owningDeployment` walks Pod→ReplicaSet→Deployment.

### `internal/audit/` — durable, append-only record of every execution
- `audit.go` — `Event` schema, `Sink` interface, `NopSink` (audit off), `FileSink` (JSONL, mutex + `O_APPEND`, optional fsync), `MultiSink`; `WithActor`/`ActorFrom` carry the triggering actor on the context.
- `executor.go` — `AuditingExecutor`: decorates any `executor.Executor`, records `executing` → `executed`/`failed` around each `Execute`, and **returns the inner error unchanged**. Wrapped once at construction inside `buildExecutor`/`buildClusterExecutor`, so it captures all three execution paths (API approval, auto-safe, interactive review). `audit` imports `executor`, never the reverse — no cycle. Enabled by `--audit-log <file>` on `monitor`/`multi-monitor`/`analyze`.

### `internal/cluster/` — multi-agent topology
- `worker.go` — `ClusterWorker` (subagent): fast collection loop + separate analyze goroutine; caches latest snapshot for the coordinator; **auto-safe** auto-execution (`processDecisions` / `autoExecute`) + per-target cooldown.
- `coordinator.go` — `Coordinator` (master): slow loop reading workers' cached snapshots, LLM cross-cluster reasoning, single-flight `busy` guard; decisions stay pending.
- `analysis.go` — shared helpers: `offerLatest` (drop-latest snapshot hand-off) + `recordDecisions` (stamp status + store).

### `internal/api/` — HTTP API + SSE
- `server.go` — `NewServer` (single) / `NewMultiServer` (fleet). Routes: `/api/cluster`, `/api/decisions`, `/api/events`, and `/api/clusters`, `/api/clusters/{c}/{snapshot,decisions,events,decisions/{id}/{approve|reject}}`. `executorFor` routes approvals by cluster; `executeDecision` runs on a fresh 30s ctx.

### `internal/store/` — in-memory state
- `store.go` — ring-buffer decision store + snapshot + SSE pub/sub (`Subscribe`/`UpdateAndBroadcast`). **In-memory only** (no persistence yet).

### `internal/ui/` — `styles.go`: lipgloss styles, banner, badges (shared by all TUIs).

### `web/` — React + TypeScript + Vite dashboard
- `src/components/` — `Dashboard.tsx`, `ClusterOverview.tsx`, `DecisionFeed.tsx`. **Still targets the single-cluster `/api/*` routes** — not yet aware of `/api/clusters/...` (open roadmap item).

---

## Known gaps / deliberate non-goals (see README Roadmap)
- Coordinator decisions + non-reversible actions stay **human-approved** (no full-auto tier).
- Dashboard is not multi-cluster-aware yet.
- Store is in-memory. Executions can be persisted with `--audit-log` (JSONL,
  `internal/audit/`), but decision *lifecycle* events (propose/approve/reject),
  log rotation, and a read API are still open.
- `evict_and_move` doesn't honor `target_node`; `patch_limits` is the only multi-container-aware action.
- `analyze --apply` / `review` are single-cluster (single provider + single non-routing executor). Multi-cluster auto-heal lives in `multi-monitor --auto-safe`.

## Handy commands
```
go build ./... && go vet ./... && go test ./...
# safe multi-cluster demo (no cluster/LLM needed):
courtvision multi-monitor --clusters mock-a,mock-b,mock-c --metrics mock --auto-safe --interval 3s
curl localhost:8080/api/clusters
```
