// Package audit provides a durable, append-only record of every action
// CourtVision executes against a cluster.
//
// Decisions live only in an in-memory ring buffer (internal/store), which is
// lost on restart. That is fine for the dashboard, but an agent that can mutate
// real clusters — especially unattended under multi-monitor --auto-safe — needs
// a persistent record of what it changed and why. This package writes one such
// record per execution attempt, tagged with who triggered it (an operator
// approval, the auto-safe loop, or the interactive review), across every path
// that runs an executor.
//
// The write path is a single decorator, AuditingExecutor (see executor.go),
// wrapped around whatever executor the safety switch already selected
// (dry-run / mock / live). Because every mutation funnels through the
// executor.Executor interface, one wrap captures them all without touching the
// call sites.
package audit

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// Event is a single audit record: one execution attempt in one of its phases.
// Each decision that runs produces an "executing" event followed by a terminal
// "executed" or "failed" one, so the log captures both intent and outcome even
// if the process dies mid-action.
type Event struct {
	Time        time.Time        `json:"time"`
	Actor       string           `json:"actor"` // who triggered it: api-approval | auto-safe | interactive-review | system
	Cluster     string           `json:"cluster"`
	DecisionID  string           `json:"decision_id"`
	Action      types.ActionType `json:"action"`
	Severity    types.Severity   `json:"severity,omitempty"`
	Namespace   string           `json:"namespace,omitempty"`
	TargetPod   string           `json:"target_pod,omitempty"`
	TargetNode  string           `json:"target_node,omitempty"`
	NewCPULimit float64          `json:"new_cpu_limit,omitempty"`
	NewMemLimit float64          `json:"new_mem_limit,omitempty"`
	Reasoning   string           `json:"reasoning,omitempty"`
	Phase       string           `json:"phase"`          // executing | executed | failed
	DryRun      bool             `json:"dry_run"`        // true when the underlying executor makes no real change
	Mode        string           `json:"mode"`           // the safety-switch label: dry-run | mock | live
	DurationMS  int64            `json:"duration_ms,omitempty"` // wall time of the execute call (terminal events only)
	Error       string           `json:"error,omitempty"`
}

// Sink is an append-only audit destination. Implementations must be safe for
// concurrent use: in multi-cluster mode every ClusterWorker shares one sink.
type Sink interface {
	Record(Event)
	io.Closer
}

// NopSink is the default when auditing is disabled (no --audit-log). It drops
// every event, so callers never have to nil-check a sink.
type NopSink struct{}

// NewNopSink returns a sink that records nothing.
func NewNopSink() *NopSink { return &NopSink{} }

func (*NopSink) Record(Event) {}
func (*NopSink) Close() error { return nil }

// FileSink appends events to a file as newline-delimited JSON (JSONL): one JSON
// object per line, so the log is both machine-parseable (jq, log pipelines) and
// human-tailable. It is opened with O_APPEND so restarts extend the same file
// rather than truncating it.
//
// A mutex serializes writes so concurrent workers never interleave a partial
// line. When fsync is set, each record is flushed to disk before Record returns
// — safest for a true audit trail, at the cost of throughput; left off, writes
// rely on the OS page cache and are far cheaper. Auditing sits off the hot
// metric-collection path (it runs inside the executor call), so the default
// wiring can afford fsync when durability matters.
type FileSink struct {
	mu    sync.Mutex
	f     *os.File
	enc   *json.Encoder
	fsync bool
}

// NewFileSink opens (creating if needed) the JSONL audit file at path in append
// mode. When fsync is true every record is flushed to stable storage before
// Record returns.
func NewFileSink(path string, fsync bool) (*FileSink, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &FileSink{f: f, enc: json.NewEncoder(f), fsync: fsync}, nil
}

// Record appends one event as a JSON line. Encoding failures are dropped rather
// than propagated: auditing must never break the execution path it observes, and
// Record has no error to return by contract. A best-effort record is the right
// tradeoff for an observability side-channel.
func (s *FileSink) Record(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// json.Encoder.Encode writes a trailing newline, giving us JSONL for free.
	if err := s.enc.Encode(e); err != nil {
		return
	}
	if s.fsync {
		_ = s.f.Sync()
	}
}

// Close flushes and closes the underlying file.
func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}

// MultiSink fans one event out to several sinks, e.g. a durable file plus stderr
// so an operator can watch actions stream by live. A failure to close one sink
// does not stop the others; the first close error is returned.
type MultiSink struct {
	sinks []Sink
}

// NewMultiSink groups sinks so a single Record reaches all of them.
func NewMultiSink(sinks ...Sink) *MultiSink { return &MultiSink{sinks: sinks} }

func (m *MultiSink) Record(e Event) {
	for _, s := range m.sinks {
		s.Record(e)
	}
}

func (m *MultiSink) Close() error {
	var first error
	for _, s := range m.sinks {
		if err := s.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// actorKey is the unexported context key under which the triggering actor is
// carried, so it never collides with keys from other packages.
type actorKey struct{}

// actorSystem is the fallback when no actor was set on the context — an
// execution with no identifiable human/loop origin (e.g. a direct call).
const actorSystem = "system"

// WithActor tags ctx with the identity of whatever is about to run an executor
// (an operator approval, the auto-safe loop, the interactive review). The
// AuditingExecutor reads it back so each record says who acted, even though all
// paths share the same executor chokepoint.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFrom returns the actor tagged on ctx, or "system" if none was set.
func ActorFrom(ctx context.Context) string {
	if a, ok := ctx.Value(actorKey{}).(string); ok && a != "" {
		return a
	}
	return actorSystem
}
