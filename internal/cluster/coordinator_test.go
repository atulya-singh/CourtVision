package cluster

import (
	"testing"
	"time"

	"github.com/atulya-singh/CourtVision/internal/store"
	"github.com/atulya-singh/CourtVision/internal/types"
)

// stubGenerator is a fake llm.Generatable: it returns a canned response and
// records whether it was called, so tests can assert both the coordinator's
// output and that it short-circuits before the LLM when it should.
type stubGenerator struct {
	resp  string
	err   error
	calls int
}

func (s *stubGenerator) Generate(string) (string, error) {
	s.calls++
	return s.resp, s.err
}

// workerWithSnapshot builds a worker whose cached snapshot is already populated,
// standing in for one that has completed a collection tick.
func workerWithSnapshot(name string, snap *types.ClusterSnapshot) *ClusterWorker {
	w := NewClusterWorker(name, nil, nil, nil, false, time.Minute, nil)
	w.latest = snap
	return w
}

func snap(name string) *types.ClusterSnapshot {
	return &types.ClusterSnapshot{ClusterName: name, Timestamp: time.Now()}
}

func TestCoordinator_TickRecordsCrossClusterDecisions(t *testing.T) {
	workers := []*ClusterWorker{
		workerWithSnapshot("c1", snap("c1")),
		workerWithSnapshot("c2", snap("c2")),
	}
	stub := &stubGenerator{
		resp: `{"action":"scale_down","target_cluster":"c2","target_pod":"p","namespace":"ns","severity":"high","reasoning":"rebalance the fleet"}`,
	}
	masterStore := store.New()
	c := NewCoordinator(workers, stub, masterStore, time.Minute)

	c.tick()

	if stub.calls != 1 {
		t.Fatalf("expected the LLM to be consulted once, got %d calls", stub.calls)
	}
	got := masterStore.GetDecisions()
	if len(got) != 1 {
		t.Fatalf("expected 1 cross-cluster decision recorded, got %d", len(got))
	}
	if got[0].Action != types.ActionScaleDown {
		t.Errorf("unexpected action: %s", got[0].Action)
	}
	// Coordinator decisions are advisory: recorded pending, never auto-executed.
	if got[0].Status != types.StatusPending {
		t.Errorf("cross-cluster decision should be pending, got %s", got[0].Status)
	}
	// ParseResponse stamps the target cluster from the LLM's target_cluster field.
	if got[0].ClusterName != "c2" {
		t.Errorf("decision should target cluster c2, got %q", got[0].ClusterName)
	}
}

func TestCoordinator_NeedsTwoClusters(t *testing.T) {
	// Only one worker has a snapshot; the other is still cold (nil).
	workers := []*ClusterWorker{
		workerWithSnapshot("c1", snap("c1")),
		workerWithSnapshot("c2", nil),
	}
	stub := &stubGenerator{resp: `{"action":"scale_down","target_pod":"p","namespace":"ns"}`}
	masterStore := store.New()
	c := NewCoordinator(workers, stub, masterStore, time.Minute)

	c.tick()

	if stub.calls != 0 {
		t.Errorf("with fewer than two snapshots the LLM should not be consulted, got %d calls", stub.calls)
	}
	if n := len(masterStore.GetDecisions()); n != 0 {
		t.Errorf("no decisions should be recorded, got %d", n)
	}
}

func TestCoordinator_SkipsNilSnapshots(t *testing.T) {
	// Three workers, one still cold: the two ready ones are enough to proceed.
	workers := []*ClusterWorker{
		workerWithSnapshot("c1", snap("c1")),
		workerWithSnapshot("c2", nil),
		workerWithSnapshot("c3", snap("c3")),
	}
	stub := &stubGenerator{
		resp: `{"action":"cordon_node","target_cluster":"c1","target_node":"n1","namespace":"ns","severity":"medium","reasoning":"drain"}`,
	}
	masterStore := store.New()
	c := NewCoordinator(workers, stub, masterStore, time.Minute)

	c.tick()

	if stub.calls != 1 {
		t.Fatalf("two ready snapshots should be enough to consult the LLM, got %d calls", stub.calls)
	}
	if n := len(masterStore.GetDecisions()); n != 1 {
		t.Errorf("expected 1 decision from the two ready clusters, got %d", n)
	}
}

func TestCoordinator_LLMErrorRecordsNothing(t *testing.T) {
	workers := []*ClusterWorker{
		workerWithSnapshot("c1", snap("c1")),
		workerWithSnapshot("c2", snap("c2")),
	}
	stub := &stubGenerator{err: errStub}
	masterStore := store.New()
	c := NewCoordinator(workers, stub, masterStore, time.Minute)

	c.tick() // must not panic

	if n := len(masterStore.GetDecisions()); n != 0 {
		t.Errorf("an LLM error should record no decisions, got %d", n)
	}
}

func TestCoordinator_NonJSONResponseRecordsNothing(t *testing.T) {
	workers := []*ClusterWorker{
		workerWithSnapshot("c1", snap("c1")),
		workerWithSnapshot("c2", snap("c2")),
	}
	stub := &stubGenerator{resp: "I could not find anything actionable."}
	masterStore := store.New()
	c := NewCoordinator(workers, stub, masterStore, time.Minute)

	c.tick()

	if n := len(masterStore.GetDecisions()); n != 0 {
		t.Errorf("a prose (non-JSON) response should yield no decisions, got %d", n)
	}
}

type stubErr string

func (e stubErr) Error() string { return string(e) }

const errStub = stubErr("ollama unreachable")
