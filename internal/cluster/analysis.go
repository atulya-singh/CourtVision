package cluster

import (
	"github.com/atulya-singh/CourtVision/internal/store"
	"github.com/atulya-singh/CourtVision/internal/types"
)

// offerLatest hands a snapshot to a single-slot channel with drop-latest
// semantics: if a previous snapshot is still waiting to be analyzed, it is
// discarded in favor of the fresher one. This lets a fast collection loop feed a
// slower analysis goroutine without ever blocking — analysis always runs against
// the most recent state, never a backlog of stale snapshots.
//
// It is safe only with a single sender (the collection loop). Because that is the
// only goroutine draining or filling the slot from the send side, the drain-then-
// send below always leaves room and terminates.
func offerLatest(ch chan *types.ClusterSnapshot, snap *types.ClusterSnapshot) {
	select {
	case ch <- snap:
	default:
		// Slot full: drop the stale snapshot, then enqueue the fresh one. The
		// analyzer may have consumed the pending snapshot between our two selects,
		// in which case the drain is a no-op and the send still fits.
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- snap:
		default:
		}
	}
}

// recordDecisions stamps each decision's status and appends it to the store.
// Decisions that propose a real action wait for human approval (StatusPending);
// informational ones (ActionNone) have nothing to approve (StatusNone).
func recordDecisions(st *store.Store, decisions []types.Decision) {
	for _, d := range decisions {
		if d.Action == types.ActionNone {
			d.Status = types.StatusNone
		} else {
			d.Status = types.StatusPending
		}
		st.AddDecision(d)
	}
}
