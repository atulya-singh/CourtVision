package main

import (
	"strings"

	"github.com/atulya-singh/CourtVision/internal/audit"
	"github.com/atulya-singh/CourtVision/internal/executor"
	"github.com/atulya-singh/CourtVision/internal/types"
	"github.com/atulya-singh/CourtVision/internal/ui"
	tea "github.com/charmbracelet/bubbletea"
)

// applyModel is the standalone interactive approval screen used by
// `analyze --apply` from a real shell. It steps through each actionable
// decision and waits for the operator to approve, reject, or skip it, running
// the executor on approvals. The in-REPL flow shares the same reviewSession and
// renderers but lives inside the REPL's own model so it does not start a nested
// Bubbletea program.
type applyModel struct {
	session    *reviewSession
	exec       executor.Executor
	sink       audit.Sink // records rejections; executions are audited inside exec
	execLabel  string
	log        []string
	working    bool // an executor call is in flight
	approveAll bool // operator chose "approve all remaining"
	auto       bool // sticky auto-accept mode: auto-run reversible actions
	quitting   bool
}

// startExec records the operator's approval to the audit trail, then returns the
// command that runs the current decision. Approvals and rejections are the human
// gates; executions themselves are audited inside the executor.
func (m applyModel) startExec() tea.Cmd {
	d := m.session.current()
	m.sink.Record(audit.Lifecycle("interactive-review", audit.PhaseApproved, "", &d))
	return runExecutor(m.exec, d)
}

// autoAdvance kicks off the next decision automatically when auto mode is on and
// the current action is reversible. It is called at every idle transition (after
// a toggle, an execution, or a manual reject/skip) so auto mode walks the queue
// on its own, pausing only on a non-reversible action that needs explicit
// approval.
func (m applyModel) autoAdvance() (applyModel, tea.Cmd) {
	if m.auto && !m.working && !m.session.done() && isReversible(m.session.current().Action) {
		m.working = true
		return m, m.startExec()
	}
	return m, nil
}

func newApplyModel(decisions []types.Decision, exec executor.Executor, label string, sink audit.Sink) applyModel {
	if sink == nil {
		sink = audit.NewNopSink()
	}
	return applyModel{
		session:   newReviewSession(decisions),
		exec:      exec,
		sink:      sink,
		execLabel: label,
	}
}

func (m applyModel) Init() tea.Cmd { return nil }

func (m applyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case execDoneMsg:
		errStr := ""
		if msg.err != nil {
			errStr = msg.err.Error()
		}
		m.session.record(msg.status, errStr)
		m.log = append(m.log, renderOutcome(m.session.outcomes[len(m.session.outcomes)-1]))
		m.working = false
		// A failure pauses "approve all" and auto mode so the operator can react
		// instead of firing the rest of the queue into a problem.
		if msg.status == types.StatusFailed {
			m.approveAll = false
			m.auto = false
		}
		if m.session.done() {
			m.quitting = true
			return m, tea.Quit
		}
		if m.approveAll {
			m.working = true
			return m, m.startExec()
		}
		return m.autoAdvance()

	case tea.KeyMsg:
		if m.working {
			return m, nil // ignore input while an action is running
		}
		switch msg.String() {
		case "tab":
			// Toggle sticky auto-accept. Turning it on runs the next reversible
			// action immediately; on a non-reversible one it just waits.
			m.auto = !m.auto
			return m.autoAdvance()
		case "a", "y":
			m.working = true
			return m, m.startExec()
		case "A":
			m.approveAll = true
			m.working = true
			return m, m.startExec()
		case "r", "n":
			// A rejection produces no execution, so the executor's audit path never
			// sees it. Record it here so the durable trail shows the operator's call.
			rejected := m.session.current()
			m.sink.Record(audit.Lifecycle("interactive-review", audit.PhaseRejected, "", &rejected))
			m.session.record(types.StatusRejected, "")
			m.log = append(m.log, renderOutcome(m.session.outcomes[len(m.session.outcomes)-1]))
			if m.session.done() {
				m.quitting = true
				return m, tea.Quit
			}
			// Resume auto mode on the next item if the operator cleared a
			// non-reversible one that had paused the queue.
			return m.autoAdvance()
		case "s":
			m.session.record(statusSkipped, "")
			m.log = append(m.log, renderOutcome(m.session.outcomes[len(m.session.outcomes)-1]))
			if m.session.done() {
				m.quitting = true
				return m, tea.Quit
			}
			return m.autoAdvance()
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m applyModel) View() string {
	var b strings.Builder
	if len(m.log) > 0 {
		b.WriteString(strings.Join(m.log, "\n"))
		b.WriteString("\n\n")
	}

	if m.quitting || m.session.done() {
		b.WriteString(renderSummary(m.session))
		b.WriteString("\n")
		return b.String()
	}

	if m.working {
		b.WriteString(renderDecisionPrompt(m.session.current(), m.session.idx+1, m.session.total()))
		b.WriteString(ui.YellowStyle.Render("  executing...") + "\n")
		return b.String()
	}

	b.WriteString(renderDecisionPrompt(m.session.current(), m.session.idx+1, m.session.total()))
	b.WriteString("\n")
	if notice := autoNotice(m.auto, m.session.current()); notice != "" {
		b.WriteString(notice + "\n")
	}
	b.WriteString(reviewHint(m.auto))
	b.WriteString("\n")
	return b.String()
}

// runApply launches the interactive approval screen for the given decisions.
// It returns nil when there is nothing actionable to review. sink records
// rejections to the audit trail (executions are audited inside exec).
func runApply(decisions []types.Decision, exec executor.Executor, label string, sink audit.Sink) error {
	m := newApplyModel(decisions, exec, label, sink)
	if m.session.total() == 0 {
		return nil
	}
	_, err := tea.NewProgram(m).Run()
	return err
}
