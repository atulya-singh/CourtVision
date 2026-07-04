package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atulya-singh/CourtVision/internal/executor"
	"github.com/atulya-singh/CourtVision/internal/metrics"
	"github.com/atulya-singh/CourtVision/internal/types"
	"github.com/atulya-singh/CourtVision/internal/ui"
	tea "github.com/charmbracelet/bubbletea"
)

// makeProvider builds a metrics source by name. Shared so every entry point
// (analyze, monitor, the REPL) speaks the same flag vocabulary.
func makeProvider(source, namespace string) (metrics.Provider, error) {
	switch source {
	case "mock":
		return metrics.NewMockProvider("mock-cluster"), nil
	case "k8s":
		return metrics.NewK8sProvider(namespace, "")
	default:
		return nil, fmt.Errorf("unknown metrics source: %s (use 'mock' or 'k8s')", source)
	}
}

// statusSkipped is a CLI-only outcome: the operator chose neither to run nor to
// reject a decision, so it stays pending for a later pass. It is not part of the
// shared types because the dashboard has no equivalent gesture.
const statusSkipped types.DecisionStatus = "skipped"

// reviewOutcome records what the operator decided for one decision and how the
// executor responded.
type reviewOutcome struct {
	decision types.Decision
	status   types.DecisionStatus // executed / failed / rejected / skipped
	err      string
}

// reviewSession walks a list of actionable decisions one at a time, like a
// review queue. It holds no UI state on purpose: keeping the approve/reject/skip
// bookkeeping separate from Bubbletea means the logic can be unit-tested, and it
// can back both the standalone command and the in-REPL flow without duplication.
type reviewSession struct {
	pending  []types.Decision
	idx      int
	outcomes []reviewOutcome
}

// newReviewSession keeps only decisions that actually propose an action.
// Informational ones (Action == none) have nothing to approve, so they never
// enter the queue.
func newReviewSession(decisions []types.Decision) *reviewSession {
	var actionable []types.Decision
	for _, d := range decisions {
		if d.Action != types.ActionNone {
			actionable = append(actionable, d)
		}
	}
	return &reviewSession{pending: actionable}
}

func (s *reviewSession) total() int              { return len(s.pending) }
func (s *reviewSession) done() bool              { return s.idx >= len(s.pending) }
func (s *reviewSession) current() types.Decision { return s.pending[s.idx] }

// record stores the outcome for the current decision and advances the cursor.
func (s *reviewSession) record(status types.DecisionStatus, err string) {
	s.outcomes = append(s.outcomes, reviewOutcome{
		decision: s.pending[s.idx],
		status:   status,
		err:      err,
	})
	s.idx++
}

// tally counts outcomes by status for the closing summary.
func (s *reviewSession) tally() map[types.DecisionStatus]int {
	out := map[types.DecisionStatus]int{}
	for _, o := range s.outcomes {
		out[o.status]++
	}
	return out
}

// buildExecutor selects how approved decisions get carried out. It is the single
// safety switch shared by every entry point: --dry-run always wins and never
// mutates anything, a real cluster gets the real executor, and anything else
// (the mock provider) gets a simulated one.
func buildExecutor(metricsSource string, dryRun bool) (executor.Executor, string, error) {
	switch {
	case dryRun:
		return executor.NewDryRunExecutor(), "dry-run (no changes)", nil
	case metricsSource == "k8s":
		e, err := executor.NewK8sExecutor("")
		if err != nil {
			return nil, "", err
		}
		return e, "LIVE Kubernetes", nil
	default:
		return executor.NewMockExecutor(), "mock (simulated)", nil
	}
}

// execDoneMsg is delivered to a Bubbletea model when an executor call finishes.
type execDoneMsg struct {
	status types.DecisionStatus
	err    error
}

// runExecutor runs one decision on a background command so the TUI stays
// responsive (and can show an "executing" state) while a cluster call is in
// flight. The 30s timeout mirrors the API server's.
func runExecutor(exec executor.Executor, d types.Decision) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := exec.Execute(ctx, &d)
		st := types.StatusExecuted
		if err != nil {
			st = types.StatusFailed
		}
		return execDoneMsg{status: st, err: err}
	}
}

// ── Shared renderers ────────────────────────────────────────────────────────

// renderDecisionPrompt shows a single decision the way an approval gate should:
// what it will do, to what, and why, so the operator can make an informed call.
func renderDecisionPrompt(d types.Decision, position, total int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %s  %s\n",
		ui.BrandStyle.Render(fmt.Sprintf("Review %d/%d", position, total)),
		ui.SeverityBadge(string(d.Severity))))
	b.WriteString(fmt.Sprintf("  %s %s\n", ui.DimStyle.Render("Pod:   "), ui.CyanStyle.Render(d.Namespace+"/"+d.TargetPod)))
	b.WriteString(fmt.Sprintf("  %s %s\n", ui.DimStyle.Render("Action:"), ui.BlueStyle.Render(string(d.Action))))
	if d.TargetNode != "" {
		b.WriteString(fmt.Sprintf("  %s %s\n", ui.DimStyle.Render("Node:  "), ui.WhiteStyle.Render(d.TargetNode)))
	}
	if d.NewCPULimit > 0 {
		b.WriteString(fmt.Sprintf("  %s %s\n", ui.DimStyle.Render("CPU →: "), ui.WhiteStyle.Render(fmt.Sprintf("%.0fm", d.NewCPULimit))))
	}
	if d.NewMemLimit > 0 {
		b.WriteString(fmt.Sprintf("  %s %s\n", ui.DimStyle.Render("Mem →: "), ui.WhiteStyle.Render(fmt.Sprintf("%.0fMB", d.NewMemLimit))))
	}
	b.WriteString(fmt.Sprintf("  %s %s\n", ui.DimStyle.Render("Why:   "), d.Reasoning))
	return b.String()
}

// isReversible delegates to the canonical action classifier in the types
// package, shared with the multi-cluster workers' auto-safe mode.
func isReversible(a types.ActionType) bool {
	return a.IsReversible()
}

func reviewHint(auto bool) string {
	state := "off"
	if auto {
		state = ui.GreenStyle.Render("on")
	}
	return ui.DimStyle.Render("  [a]pprove  [r]eject  [s]kip  [A]pprove all  [tab] auto: ") +
		state + ui.DimStyle.Render("  [q]uit")
}

// autoNotice explains, while auto mode is on, why the queue has paused on a
// decision that auto mode will not run on its own. It returns an empty string
// when there is nothing to flag (auto off, or the current action is reversible).
func autoNotice(auto bool, d types.Decision) string {
	if !auto || isReversible(d.Action) {
		return ""
	}
	return ui.YellowStyle.Render(fmt.Sprintf("  auto on — %s needs explicit approval", d.Action))
}

// renderOutcome formats one finished decision for the running log.
func renderOutcome(o reviewOutcome) string {
	var marker, label string
	switch o.status {
	case types.StatusExecuted:
		marker, label = ui.CheckMark, ui.GreenStyle.Render("executed")
	case types.StatusFailed:
		marker, label = ui.CrossMark, ui.RedStyle.Render("failed: "+o.err)
	case types.StatusRejected:
		marker, label = ui.CrossMark, ui.DimStyle.Render("rejected")
	default:
		marker, label = ui.Dot, ui.DimStyle.Render("skipped")
	}
	return fmt.Sprintf("  %s %s %s",
		marker,
		ui.CyanStyle.Render(o.decision.Namespace+"/"+o.decision.TargetPod),
		label)
}

func renderSummary(s *reviewSession) string {
	t := s.tally()
	return fmt.Sprintf("  %s %d executed · %d failed · %d rejected · %d skipped",
		ui.BrandStyle.Render("Review complete:"),
		t[types.StatusExecuted], t[types.StatusFailed], t[types.StatusRejected], t[statusSkipped])
}
