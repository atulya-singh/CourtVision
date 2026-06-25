package main

import (
	"strings"

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
	execLabel  string
	log        []string
	working    bool // an executor call is in flight
	approveAll bool // operator chose "approve all remaining"
	quitting   bool
}

func newApplyModel(decisions []types.Decision, exec executor.Executor, label string) applyModel {
	return applyModel{
		session:   newReviewSession(decisions),
		exec:      exec,
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
		// A failure pauses "approve all" so the operator can react instead of
		// firing the rest of the queue into a problem.
		if msg.status == types.StatusFailed {
			m.approveAll = false
		}
		if m.session.done() {
			m.quitting = true
			return m, tea.Quit
		}
		if m.approveAll {
			m.working = true
			return m, runExecutor(m.exec, m.session.current())
		}
		return m, nil

	case tea.KeyMsg:
		if m.working {
			return m, nil // ignore input while an action is running
		}
		switch msg.String() {
		case "a", "y":
			m.working = true
			return m, runExecutor(m.exec, m.session.current())
		case "A":
			m.approveAll = true
			m.working = true
			return m, runExecutor(m.exec, m.session.current())
		case "r", "n":
			m.session.record(types.StatusRejected, "")
			m.log = append(m.log, renderOutcome(m.session.outcomes[len(m.session.outcomes)-1]))
			if m.session.done() {
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		case "s":
			m.session.record(statusSkipped, "")
			m.log = append(m.log, renderOutcome(m.session.outcomes[len(m.session.outcomes)-1]))
			if m.session.done() {
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
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
	b.WriteString(reviewHint())
	b.WriteString("\n")
	return b.String()
}

// runApply launches the interactive approval screen for the given decisions.
// It returns nil when there is nothing actionable to review.
func runApply(decisions []types.Decision, exec executor.Executor, label string) error {
	m := newApplyModel(decisions, exec, label)
	if m.session.total() == 0 {
		return nil
	}
	_, err := tea.NewProgram(m).Run()
	return err
}
