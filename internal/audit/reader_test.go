package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/atulya-singh/CourtVision/internal/types"
)

func ev(id, cluster, phase string) Event {
	return Event{DecisionID: id, Cluster: cluster, Phase: phase}
}

func TestMemorySink_SnapshotNewestFirst(t *testing.T) {
	m := NewMemorySink(10)
	m.Record(ev("1", "a", PhaseProposed))
	m.Record(ev("2", "a", PhaseExecuting))
	m.Record(ev("3", "b", PhaseExecuted))

	got := m.Snapshot()
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d", len(got))
	}
	// Newest first.
	if got[0].DecisionID != "3" || got[1].DecisionID != "2" || got[2].DecisionID != "1" {
		t.Errorf("want newest-first [3 2 1], got [%s %s %s]", got[0].DecisionID, got[1].DecisionID, got[2].DecisionID)
	}
}

func TestMemorySink_RingEvicts(t *testing.T) {
	m := NewMemorySink(2)
	m.Record(ev("1", "a", PhaseProposed))
	m.Record(ev("2", "a", PhaseProposed))
	m.Record(ev("3", "a", PhaseProposed)) // evicts "1"

	got := m.Snapshot()
	if len(got) != 2 {
		t.Fatalf("want 2 (capacity), got %d", len(got))
	}
	if got[0].DecisionID != "3" || got[1].DecisionID != "2" {
		t.Errorf("want [3 2] after eviction, got [%s %s]", got[0].DecisionID, got[1].DecisionID)
	}
}

func TestMemorySink_SnapshotForCluster(t *testing.T) {
	m := NewMemorySink(10)
	m.Record(ev("1", "a", PhaseProposed))
	m.Record(ev("2", "b", PhaseProposed))
	m.Record(ev("3", "a", PhaseProposed))

	got := m.SnapshotForCluster("a")
	if len(got) != 2 {
		t.Fatalf("want 2 events for cluster a, got %d", len(got))
	}
	if got[0].DecisionID != "3" || got[1].DecisionID != "1" {
		t.Errorf("want cluster-a [3 1] newest-first, got [%s %s]", got[0].DecisionID, got[1].DecisionID)
	}
}

// MemorySink must satisfy the Reader interface the API depends on.
func TestMemorySink_IsReader(t *testing.T) {
	var _ Reader = NewMemorySink(1)
}

func TestMemorySink_ConcurrentRecord(t *testing.T) {
	m := NewMemorySink(1000)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				m.Record(ev("x", "c", PhaseExecuted))
			}
		}()
	}
	wg.Wait()
	if got := len(m.Snapshot()); got != 1000 {
		t.Errorf("want 1000 events retained, got %d", got)
	}
}

func TestLifecycle_CarriesDecisionFieldsNoExecFields(t *testing.T) {
	d := &types.Decision{
		ID: "d1", ClusterName: "c1", Action: types.ActionScaleDown,
		Namespace: "ns", TargetPod: "p", Reasoning: "why",
	}
	e := Lifecycle("interactive-review", PhaseRejected, "", d)

	if e.Phase != PhaseRejected || e.Actor != "interactive-review" {
		t.Errorf("unexpected phase/actor: %q/%q", e.Phase, e.Actor)
	}
	if e.Cluster != "c1" { // falls back to decision's ClusterName when "" passed
		t.Errorf("cluster fallback failed: %q", e.Cluster)
	}
	if e.DecisionID != "d1" || e.TargetPod != "p" || e.Reasoning != "why" {
		t.Errorf("decision fields not copied: %+v", e)
	}
	// Lifecycle events are not executions: no mode/duration/dry-run.
	if e.Mode != "" || e.DurationMS != 0 || e.DryRun {
		t.Errorf("lifecycle event must not carry execution fields: %+v", e)
	}
}

// TestFileSink_Rotates verifies size-based rotation: once a write would push the
// file past maxBytes a fresh file starts, the previous one is preserved as a
// numbered backup, no file exceeds the cap (records are smaller than it), and no
// more than `backups` backups are kept.
func TestFileSink_Rotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	const cap = 400 // comfortably larger than one ~160-byte record
	const backups = 3
	sink, err := NewFileSink(path, false, cap, backups)
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	for i := 0; i < 20; i++ {
		sink.Record(ev("id", "cluster-with-a-longish-name", PhaseExecuted))
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The live file and at least the first backup exist.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("live audit file missing after rotation: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("expected rotated backup %s.1: %v", path, err)
	}

	// Invariant: with every record smaller than the cap, no file exceeds the cap.
	for i := 0; i <= backups; i++ {
		name := path
		if i > 0 {
			name = fmt.Sprintf("%s.%d", path, i)
		}
		if fi, err := os.Stat(name); err == nil && fi.Size() > cap {
			t.Errorf("%s exceeds cap: %d > %d", name, fi.Size(), cap)
		}
	}

	// Retention: never more than `backups` numbered files, so .{backups+1} is absent.
	if _, err := os.Stat(fmt.Sprintf("%s.%d", path, backups+1)); !os.IsNotExist(err) {
		t.Errorf("expected at most %d backups; %s.%d should not exist", backups, path, backups+1)
	}
}

// TestFileSink_NoRotationWhenDisabled confirms maxBytes==0 keeps appending to a
// single file (the original unbounded behavior).
func TestFileSink_NoRotationWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	sink, err := NewFileSink(path, false, 0, 3)
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	for i := 0; i < 20; i++ {
		sink.Record(ev("id", "c", PhaseExecuted))
	}
	_ = sink.Close()

	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("no rotation expected with maxBytes=0, but %s.1 exists", path)
	}
	if lines := readLines(t, path); len(lines) != 20 {
		t.Errorf("want 20 lines in single file, got %d", len(lines))
	}
}
