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
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// Phase names the point in a decision's life that an Event captures. Execution
// events (executing → executed/failed) come from the AuditingExecutor; lifecycle
// events (rejected, ...) are emitted directly by the flow that made the call.
const (
	PhaseProposed  = "proposed"  // recorded but not yet acted on (reserved; see note in the reader)
	PhaseApproved  = "approved"  // an operator authorized the action (reserved)
	PhaseRejected  = "rejected"  // an operator declined the action — no execution follows
	PhaseExecuting = "executing" // the executor is about to run
	PhaseExecuted  = "executed"  // the executor finished successfully
	PhaseFailed    = "failed"    // the executor returned an error
)

// Event is a single audit record: one point in a decision's lifecycle. An
// execution produces an "executing" event followed by a terminal "executed" or
// "failed" one, so the log captures both intent and outcome even if the process
// dies mid-action. Lifecycle events (e.g. "rejected") stand alone.
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
//
// When maxBytes > 0 the sink rotates: before a write that would push the file
// past maxBytes it renames the current file to "<path>.1" (shifting any existing
// "<path>.N" up, discarding the oldest beyond backups) and starts a fresh file,
// so an unattended agent can't fill the disk. maxBytes == 0 keeps the original
// unbounded append behavior.
type FileSink struct {
	mu       sync.Mutex
	f        *os.File
	path     string
	fsync    bool
	maxBytes int64
	backups  int
	size     int64 // bytes in the current file; seeded from its size on open
}

// NewFileSink opens (creating if needed) the JSONL audit file at path in append
// mode. When fsync is true every record is flushed to stable storage before
// Record returns. maxBytes > 0 enables size-based rotation keeping up to backups
// old files ("<path>.1" .. "<path>.N"); maxBytes == 0 disables rotation.
func NewFileSink(path string, fsync bool, maxBytes int64, backups int) (*FileSink, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	// Seed size from the existing file so an appended-to log rotates at the right
	// point rather than only counting bytes written this run.
	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	return &FileSink{f: f, path: path, fsync: fsync, maxBytes: maxBytes, backups: backups, size: size}, nil
}

// Record appends one event as a JSON line, rotating first if the write would
// exceed maxBytes. Failures are dropped rather than propagated: auditing must
// never break the execution path it observes, and Record has no error to return
// by contract. A best-effort record is the right tradeoff for an observability
// side-channel.
func (s *FileSink) Record(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	data = append(data, '\n') // JSONL: one object per line

	// Rotate before writing so no single line straddles two files. Only rotate a
	// non-empty file, so a lone oversized record still lands somewhere.
	if s.maxBytes > 0 && s.size > 0 && s.size+int64(len(data)) > s.maxBytes {
		if err := s.rotate(); err != nil {
			// Rotation failed (e.g. rename error): keep writing to the current file
			// rather than losing the record.
		}
	}

	n, err := s.f.Write(data)
	if err != nil {
		return
	}
	s.size += int64(n)
	if s.fsync {
		_ = s.f.Sync()
	}
}

// rotate closes the current file, shifts the numbered backups up by one
// (discarding the oldest beyond backups), renames the live file to "<path>.1",
// and opens a fresh empty file. The caller must hold s.mu.
func (s *FileSink) rotate() error {
	if err := s.f.Close(); err != nil {
		return err
	}
	// Shift .(backups-1) -> .backups, ..., .1 -> .2. Each rename overwrites the
	// destination, so the file that was .backups is discarded.
	for i := s.backups - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", s.path, i), fmt.Sprintf("%s.%d", s.path, i+1))
	}
	if s.backups >= 1 {
		_ = os.Rename(s.path, s.path+".1")
	} else {
		_ = os.Remove(s.path)
	}

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	s.f = f
	s.size = 0
	return nil
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

// Reader exposes recorded events for the read-only /api/audit endpoints. A sink
// that keeps events in memory (MemorySink) implements it; a pure file sink does
// not, so the API only serves audit when an in-memory reader is wired in.
type Reader interface {
	// Snapshot returns recent events, newest first.
	Snapshot() []Event
	// SnapshotForCluster returns recent events for one cluster, newest first.
	SnapshotForCluster(cluster string) []Event
}

// MemorySink keeps the most recent events in a bounded ring so the read API can
// serve them without touching the (optional, possibly rotated) file. It is both
// a Sink and a Reader: wire it alongside a FileSink via MultiSink and the same
// stream feeds durable storage and the dashboard. Because it is bounded it is
// the volatile, queryable view; the file is the durable one.
type MemorySink struct {
	mu   sync.RWMutex
	buf  []Event
	head int // next write position
	size int // number of events currently held
}

// NewMemorySink returns a ring holding up to capacity recent events.
func NewMemorySink(capacity int) *MemorySink {
	if capacity < 1 {
		capacity = 1
	}
	return &MemorySink{buf: make([]Event, capacity)}
}

func (m *MemorySink) Record(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buf[m.head] = e
	m.head = (m.head + 1) % len(m.buf)
	if m.size < len(m.buf) {
		m.size++
	}
}

func (*MemorySink) Close() error { return nil }

// Snapshot returns the held events newest-first, as a copy the caller owns.
func (m *MemorySink) Snapshot() []Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Event, 0, m.size)
	// Walk backwards from the most recent write so the result is newest-first.
	for i := 0; i < m.size; i++ {
		idx := (m.head - 1 - i + len(m.buf)) % len(m.buf)
		out = append(out, m.buf[idx])
	}
	return out
}

// SnapshotForCluster returns the held events for one cluster, newest-first.
func (m *MemorySink) SnapshotForCluster(cluster string) []Event {
	all := m.Snapshot()
	out := make([]Event, 0, len(all))
	for _, e := range all {
		if e.Cluster == cluster {
			out = append(out, e)
		}
	}
	return out
}

// Lifecycle builds a non-execution audit event marking a point in a decision's
// life (e.g. PhaseRejected). Unlike execution events it carries no Mode/DryRun/
// Duration — those describe an executor run, which a lifecycle transition is not.
// cluster falls back to the decision's own ClusterName so single-cluster callers
// can pass "".
func Lifecycle(actor, phase, cluster string, d *types.Decision) Event {
	if cluster == "" {
		cluster = d.ClusterName
	}
	return Event{
		Time:       time.Now(),
		Actor:      actor,
		Cluster:    cluster,
		DecisionID: d.ID,
		Action:     d.Action,
		Severity:   d.Severity,
		Namespace:  d.Namespace,
		TargetPod:  d.TargetPod,
		TargetNode: d.TargetNode,
		Reasoning:  d.Reasoning,
		Phase:      phase,
	}
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
