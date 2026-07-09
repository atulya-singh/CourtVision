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
   decisions over a **read-only** HTTP API + SSE dashboard. `monitor` is
   observe-only: it has no execution path (no `--dry-run`/`--audit-log` flags),
   so decisions just surface. Execute via `analyze --apply`, REPL `review`, or
   `multi-monitor --auto-safe`.
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
| `evict_and_move` | **Evict** the pod via the Eviction API — honors PodDisruptionBudgets (controller reschedules); `target_node` validated as a destination precondition (refuses missing/cordoned/NotReady targets, no-ops if already there), but placement is not force-pinned | **no** |

**Decision lifecycle:** `none` (informational) / `pending` → `executing` →
`executed` | `failed` | `rejected`.

**Safety model (layered):**
- Nothing mutates a cluster until an `Executor` runs a decision.
- **`--dry-run` (default ON)** = the safety switch: `DryRunExecutor` logs "would…"
  and changes nothing. `mock` metrics → `MockExecutor`. Only `k8s` + LIVE →
  real `K8sExecutor`.
- **The HTTP API never mutates.** It is GET + SSE only (no approve/reject route) —
  the dashboard observes, it doesn't act. Every execution path is a
  locally-authenticated process, not an anonymous browser:
  - Interactive review (`analyze --apply`, REPL `review`) — `a`/`r`/`s` per item,
    `A` = approve-all, **Tab = sticky auto-accept** (reversible actions only;
    pauses on `evict_and_move`; turns off on failure).
- **`--auto-safe` (multi-monitor, opt-in)** — each worker auto-executes its own
  **reversible** decisions unattended; `evict_and_move` stays pending; a
  **per-target cooldown** (`--auto-cooldown`, default 3m) stops the ~5s loop from
  re-firing the same fix. Coordinator (cross-cluster) decisions are surfaced
  read-only/advisory — they have no execution path of their own.

**Reversible gate:** `types.ActionType.IsReversible()` is the single source of
truth (cordon/scale_down/patch_limits = true). Used by both the interactive
auto-accept and worker auto-safe.

**Audit trail:** the full lifecycle is recorded — `proposed` (worker analysis,
**deduped by problem signature within the cooldown window** so per-tick re-analysis
with churning IDs can't flood the log), `approved`/`rejected` (interactive review
gates), and `executing` → `executed`/`failed` (executions, via the
`AuditingExecutor` decorator over the safety-switch executor). Each event is
tagged with the actor (`worker-analysis`/`interactive-review`/`auto-safe`),
cluster, action, target, reasoning, mode, duration, and error. Records always land
in an in-memory ring (`MemorySink`), surfaced read-only at **`/api/audit`** +
`/api/clusters/{c}/audit` in multi-monitor; `--audit-log <file>` (opt-in, on
`multi-monitor`/`analyze`) *also* persists them as durable JSONL, with optional
size-based rotation (`--audit-max-bytes`). The whole stream is **hash-chained**
(`HashingSink`, sha256 `prev_hash`+`hash` per event) so tampering is detectable
via `audit.VerifyChain` / the `courtvision audit-verify <file>` command. Coordinator
(cross-cluster) proposals are advisory and not yet audited. See `internal/audit/`.

---

## File map

### `cmd/courtvision/` — CLI (Cobra) + terminal UIs (package `main`)
- `main.go` — root command; registers subcommands; launches REPL when run with no args.
- `monitor.go` — `monitor` subcommand: single-cluster loop + **read-only** API server (no executor/audit/`--dry-run` wiring — observe-only). `styledMonitorLoop` collects on interval and analyzes on a **separate goroutine** (non-blocking).
- `multi.go` — `multi-monitor` subcommand: builds one worker per `--clusters` context + a coordinator; flags incl. `--auto-safe` / `--auto-cooldown`; `buildClusterProvider` / `buildClusterExecutor`.
- `analyze.go` — `analyze` one-shot: spinner model, prints table/JSON; `--apply` hands decisions to the interactive review.
- `approve.go` — shared review plumbing: `reviewSession` (pure queue), `buildExecutor` safety switch, `runExecutor`, renderers, `isReversible` (delegates to `types`).
- `apply.go` — `applyModel`: standalone Bubbletea review screen for `analyze --apply`. Has the Tab auto-accept toggle + `autoAdvance`. **Can run LIVE.**
- `repl.go` — interactive REPL; `review` runs the inline approval flow (sandboxed: mock→mock, k8s→dry-run). Mirrors `apply.go`'s auto-accept. **Slash-command palette:** typing `/` opens a live-filtered, ↑/↓-navigable dropdown (`refreshPalette`/`completeSelected`/`paletteChoice`); `Tab` completes, `Enter` runs the highlight, `Esc` dismisses; `↑/↓` still cycle history when the palette is closed. **Session modes:** `metrics`/`namespace` persist across commands (set via `/metrics`, `/namespace`, or `Shift+Tab` to toggle mock↔k8s), shown in a `renderModeBar` status line and folded into each command by `dispatch` (bare words still resolve for back-compat). Long-running servers (`/monitor`, `/multi-monitor`) print a shell hint instead of blocking the event loop.
- `slash.go` — the REPL command registry (`slashCommand`/`cmdKind`/`slashCommands`): single source of truth shared by the palette, the `dispatch` router, and `renderHelp`, so they never drift. `matchSlash` (prefix/alias filter), `lookupSlash` (exact resolve), `cobraArgsFor` (mode→flags), `externalHint`, `renderPalette`.
- `status.go` — `status` subcommand (Ollama/K8s connectivity check).
- `audit_verify.go` — `audit-verify <file>` subcommand: parses a JSONL audit log and runs `audit.VerifyChain`, reporting the first tampered/dropped event (non-zero exit) or confirming the chain is intact.
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
- `k8s.go` — `K8sExecutor`: real mutations. `patchLimits` distributes the pod-level target across containers proportionally (`distribute`/`setContainerLimits` helpers); `owningWorkload` resolves the pod's **top-level controller** (`controllerOf` prefers the `Controller:true` owner ref) — Pod→ReplicaSet→Deployment, or a StatefulSet/DaemonSet owning its pods directly, or a bare ReplicaSet. `patch_limits` supports Deployment/StatefulSet/DaemonSet/ReplicaSet; `scale_down` supports Deployment/StatefulSet/ReplicaSet (DaemonSet rejected — no replica count) via `decrementedReplicas`; CRD-owned workloads (e.g. Argo Rollout) and bare pods fail with a clear `unsupportedWorkload` error. `evict` submits a PDB-respecting Eviction (policy/v1); when `target_node` is set, `evaluateMoveTarget` validates it first (`nodeReady` helper) — refuses a missing/cordoned/NotReady node and no-ops when the pod is already there — but does not force scheduler placement. `patch_limits`/`scale_down`/`cordon_node` run under `retry.RetryOnConflict` (Get→mutate→Update refetches per attempt), so a concurrent modification 409 is retried instead of failing; non-conflict errors return immediately. `evict` submits an Eviction (policy/v1) via `EvictV1`, not a raw Delete, so it honors PodDisruptionBudgets: 429 (PDB block) → clear wrapped error (never falls back to Delete), 404 (already gone) → no-op success.

### `internal/audit/` — durable, append-only record of every execution + lifecycle
- `audit.go` — `Event` schema (with `Phase*` consts + `PrevHash`/`Hash`), `Sink` interface, `NopSink` (audit off), `FileSink` (JSONL, mutex + `O_APPEND`, optional fsync, **size-based rotation** when `maxBytes>0` keeping `backups` numbered files via `rotate`), `MultiSink`. `MemorySink` is a **bounded ring that is both a `Sink` and a `Reader`** (`Snapshot`/`SnapshotForCluster`, newest-first) — it backs the read API and works even without a file. `HashingSink` wraps the fan-out outermost and **hash-chains** every event (`prev_hash`+`hash`, serialized order == stored order); `VerifyChain([]Event)` recomputes the chain and reports the first tampered/dropped event. `Lifecycle(actor,phase,cluster,d)` builds a non-execution event (no mode/duration). `WithActor`/`ActorFrom` carry the triggering actor on the context.
- `executor.go` — `AuditingExecutor`: decorates any `executor.Executor`, records `executing` → `executed`/`failed` around each `Execute`, and **returns the inner error unchanged**. Wrapped once at construction inside `buildExecutor`/`buildClusterExecutor`, so it captures both execution paths (auto-safe, interactive review). `audit` imports `executor`, never the reverse — no cycle.
- **Wiring**: `buildAuditSink(path, maxBytes)` always builds a `MemorySink` (so `/api/audit` is live even with no `--audit-log`) and, when a path is set, fans out to a rotating `FileSink` too via `MultiSink`; it returns the reader the API serves. `--audit-log`/`--audit-max-bytes` on `multi-monitor`/`analyze` (not `monitor`, which is observe-only). **Lifecycle coverage:** the interactive review emits a `rejected` event (rejections run no executor, so they'd otherwise leave no record); `proposed` is deliberately **not** emitted per analyze tick — decision IDs churn every tick, so it would flood a durable log (proposals live in the volatile store/SSE).

### `internal/cluster/` — multi-agent topology
- `worker.go` — `ClusterWorker` (subagent): fast collection loop + separate analyze goroutine; caches latest snapshot for the coordinator; **auto-safe** auto-execution (`processDecisions` / `autoExecute`) + per-target cooldown; emits deduped `proposed` audit events (`logProposed`, keyed by `cooldownKey`, throttled by `lastProposed`).
- `coordinator.go` — `Coordinator` (master): slow loop reading workers' cached snapshots, LLM cross-cluster reasoning, single-flight `busy` guard; decisions stay pending (advisory — no execution path). `coordinator_test.go` drives `tick` with a stub `Generatable`.
- `analysis.go` — shared helpers: `offerLatest` (drop-latest snapshot hand-off) + `recordDecisions` (stamp status + store).

### `internal/api/` — **read-only** HTTP API + SSE
- `server.go` — `NewServer(store, port)` (single) / `NewMultiServer(workers, masterStore, auditReader, port)` (fleet). **GET + SSE only — no mutating routes.** Routes: `/api/cluster`, `/api/decisions`, `/api/events`, `/api/health`, and (fleet) `/api/clusters`, `/api/clusters/{c}/{snapshot,decisions,events,audit}`, plus fleet-wide `/api/audit`. The audit routes are registered only when an `audit.Reader` is wired in (multi-monitor), serving the shared `MemorySink` newest-first. `routes()` builds the mux (testable); CORS advertises `GET, OPTIONS`. The server holds no executor — approve/reject and the old `executeDecision`/`executorFor` routing were removed so a browser can never trigger a mutation. `server_test.go` has a `TestMutationRoutesRemoved` regression guard + `TestHandleAudit_*`.

### `internal/store/` — in-memory state
- `store.go` — ring-buffer decision store + snapshot + SSE pub/sub (`Subscribe`/`UpdateAndBroadcast`). **In-memory only** (no persistence yet).

### `internal/ui/` — `styles.go`: lipgloss styles, banner, badges (shared by all TUIs).

### `web/` — React + TypeScript + Vite dashboard
- `src/components/` — `Dashboard.tsx`, `ClusterOverview.tsx`, `DecisionFeed.tsx`; `src/scope.ts` (scope model + `endpointsFor`). **Read-only viewer** (no approve/reject buttons; a pending card just shows "awaiting CLI/auto-safe"). **Multi-cluster-aware**: `Dashboard` probes `GET /api/clusters` on mount (404 → single-cluster, renders the classic `/api/*` view; 200 → multi-cluster, renders a Fleet tab + one tab per worker). `endpointsFor(scope)` maps the selected scope to the `{snapshot,decisions,events}` URLs — a cluster tab hits `/api/clusters/{name}/...`, Fleet/single hit `/api/*`. `ClusterOverview`/`DecisionFeed`/`StatsBar` take their data URL as a prop and are remounted via `key` on a scope switch (clean reset, no setState-in-effect). The Fleet view shows a clickable `FleetSummary` roll-up + the coordinator's cross-cluster decisions.

---

## Known gaps / deliberate non-goals (see README Roadmap)
- Coordinator (cross-cluster) decisions are **advisory / read-only** — surfaced but with no execution path of their own; non-reversible actions stay manual (no full-auto tier).
- Dashboard is **read-only** (view metrics + decisions; no mutation). It is now multi-cluster-aware (Fleet + per-cluster tabs via `/api/clusters/...`).
- Store is in-memory. Executions can be persisted with `--audit-log` (JSONL,
  `internal/audit/`), but decision *lifecycle* events (propose/approve/reject),
  log rotation, and a read API are still open.
- `evict_and_move` validates `target_node` as a destination precondition but does not force-pin placement (the "move" is best-effort; the scheduler still owns final placement), and it evicts via the PDB-respecting Eviction API; `patch_limits` is the only multi-container-aware action. `patch_limits`/`scale_down` now resolve non-Deployment workloads too (StatefulSet/DaemonSet/ReplicaSet); CRD controllers like Argo Rollouts remain unsupported (fail with a clear error).
- `analyze --apply` / `review` are single-cluster (single provider + single non-routing executor). Multi-cluster auto-heal lives in `multi-monitor --auto-safe`.

## Handy commands
```
go build ./... && go vet ./... && go test ./...
# safe multi-cluster demo (no cluster/LLM needed):
courtvision multi-monitor --clusters mock-a,mock-b,mock-c --metrics mock --auto-safe --interval 3s
curl localhost:8080/api/clusters
```
