package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// readLines returns every non-empty line of the file at path, so tests can
// assert one JSON object was written per Record call.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audit file: %v", err)
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if sc.Text() != "" {
			lines = append(lines, sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan audit file: %v", err)
	}
	return lines
}

func TestFileSink_WritesOneJSONLinePerRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink, err := NewFileSink(path, false)
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}

	sink.Record(Event{DecisionID: "a", Action: types.ActionScaleDown, Phase: "executed"})
	sink.Record(Event{DecisionID: "b", Action: types.ActionCordonNode, Phase: "failed", Error: "boom"})
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("want 2 JSONL lines, got %d", len(lines))
	}

	// Each line must be a standalone, well-formed JSON object round-tripping back
	// to the event we wrote.
	var got Event
	if err := json.Unmarshal([]byte(lines[1]), &got); err != nil {
		t.Fatalf("line is not valid JSON: %v", err)
	}
	if got.DecisionID != "b" || got.Phase != "failed" || got.Error != "boom" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestFileSink_AppendsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")

	first, err := NewFileSink(path, false)
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	first.Record(Event{DecisionID: "1"})
	first.Close()

	// Re-opening the same path must extend the log, not truncate it — a restart
	// should never lose earlier records.
	second, err := NewFileSink(path, false)
	if err != nil {
		t.Fatalf("reopen NewFileSink: %v", err)
	}
	second.Record(Event{DecisionID: "2"})
	second.Close()

	if lines := readLines(t, path); len(lines) != 2 {
		t.Fatalf("append across reopen: want 2 lines, got %d", len(lines))
	}
}

func TestFileSink_ConcurrentRecordsAreWellFormed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink, err := NewFileSink(path, false)
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}

	// Many goroutines (mimicking multiple ClusterWorkers sharing one sink) must
	// never interleave a partial line. Run under -race to catch data races.
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sink.Record(Event{DecisionID: "d", Phase: "executed"})
		}(i)
	}
	wg.Wait()
	sink.Close()

	lines := readLines(t, path)
	if len(lines) != n {
		t.Fatalf("want %d lines, got %d", n, len(lines))
	}
	for i, l := range lines {
		var e Event
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("line %d not valid JSON under concurrency: %v", i, err)
		}
	}
}

func TestNopSink_WritesNothing(t *testing.T) {
	sink := NewNopSink()
	sink.Record(Event{DecisionID: "x"}) // must not panic or write anywhere
	if err := sink.Close(); err != nil {
		t.Errorf("NopSink.Close: %v", err)
	}
}

func TestMultiSink_FansOut(t *testing.T) {
	a, b := &countingSink{}, &countingSink{}
	m := NewMultiSink(a, b)
	m.Record(Event{})
	m.Record(Event{})
	if a.records != 2 || b.records != 2 {
		t.Errorf("fan-out mismatch: a=%d b=%d, want 2/2", a.records, b.records)
	}
	if err := m.Close(); err != nil {
		t.Errorf("MultiSink.Close: %v", err)
	}
	if a.closed != 1 || b.closed != 1 {
		t.Errorf("each sink should close once: a=%d b=%d", a.closed, b.closed)
	}
}

func TestActor_RoundTripAndDefault(t *testing.T) {
	if a := ActorFrom(context.Background()); a != actorSystem {
		t.Errorf("unset actor should default to %q, got %q", actorSystem, a)
	}
	ctx := WithActor(context.Background(), "auto-safe")
	if a := ActorFrom(ctx); a != "auto-safe" {
		t.Errorf("actor round-trip: want auto-safe, got %q", a)
	}
}

// countingSink is a trivial in-memory Sink for asserting fan-out and close.
type countingSink struct {
	records int
	closed  int
}

func (c *countingSink) Record(Event) { c.records++ }
func (c *countingSink) Close() error { c.closed++; return nil }
