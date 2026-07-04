package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/atulya-singh/CourtVision/internal/audit"
	"github.com/atulya-singh/CourtVision/internal/decision"
	"github.com/atulya-singh/CourtVision/internal/executor"
	"github.com/atulya-singh/CourtVision/internal/llm"
	"github.com/atulya-singh/CourtVision/internal/types"
	"github.com/atulya-singh/CourtVision/internal/ui"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	errorStyle = lipgloss.NewStyle().Foreground(ui.Red)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(ui.DimGray)

	statusDotGreen = lipgloss.NewStyle().
			Foreground(ui.Green).
			Render("●")

	statusDotRed = lipgloss.NewStyle().
			Foreground(ui.Red).
			Render("●")

	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ui.Purple).
			Padding(0, 1)

	inputLabelStyle = lipgloss.NewStyle().
			Foreground(ui.Purple).
			Bold(true)

	goodbyeStyle = lipgloss.NewStyle().
			Foreground(ui.Green).
			Bold(true)
)

// ── Status check ──────────────────────────────────────────────────────────────

type connStatus struct {
	ollamaOK bool
	k8sOK    bool
}

type statusMsg connStatus

func checkConnStatus() tea.Msg {
	s := connStatus{}

	// Check Ollama
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:11434/api/tags")
	if err == nil {
		resp.Body.Close()
		s.ollamaOK = resp.StatusCode == http.StatusOK
	}

	return statusMsg(s)
}

// ── Model ─────────────────────────────────────────────────────────────────────

// replMode is what the REPL is currently doing. In modeInput it behaves like a
// normal prompt; in modeReview it temporarily becomes an approval screen that
// walks the operator through proposed decisions, like Claude Code's accept/reject
// flow. Reusing the same Bubbletea program (rather than launching a nested one)
// is what makes the inline review possible.
type replMode int

const (
	modeInput replMode = iota
	modeReview
)

type replModel struct {
	textInput textinput.Model
	rootCmd   *cobra.Command
	history   []string
	histIdx   int
	output    []string // lines of output from commands
	status    connStatus
	width     int
	quitting  bool

	// review-mode state
	mode      replMode
	session   *reviewSession
	exec      executor.Executor
	reviewAll bool // operator chose "approve all remaining"
	auto      bool // sticky auto-accept mode: auto-run reversible actions
	working   bool // an executor call is in flight
}

// autoAdvance runs the next decision automatically when auto mode is on and the
// current action is reversible. Called at every idle transition so auto mode
// walks the queue on its own, pausing only on a non-reversible action that needs
// explicit approval.
func (m replModel) autoAdvance() (tea.Model, tea.Cmd) {
	if m.auto && !m.working && m.session != nil && !m.session.done() && isReversible(m.session.current().Action) {
		m.working = true
		return m, runExecutor(m.exec, m.session.current())
	}
	return m, nil
}

func newREPL(rootCmd *cobra.Command) replModel {
	ti := textinput.New()
	ti.Prompt = lipgloss.NewStyle().Foreground(ui.Cyan).Bold(true).Render("› ")
	ti.Focus()
	ti.CharLimit = 256
	ti.Width = 70

	return replModel{
		textInput: ti,
		rootCmd:   rootCmd,
		history:   []string{},
		histIdx:   -1,
		width:     80,
	}
}

func (m replModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, checkConnStatus)
}

func (m replModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		boxInner := m.width - 4 // border + padding
		if boxInner < 20 {
			boxInner = 20
		}
		m.textInput.Width = boxInner - 4 // account for prompt chars
		return m, nil

	case statusMsg:
		m.status = connStatus(msg)
		return m, nil

	case reviewLoadedMsg:
		return m.onReviewLoaded(msg)

	case execDoneMsg:
		return m.onExecDone(msg)

	case tea.KeyMsg:
		// In review mode the keyboard drives the approval flow, not the prompt.
		if m.mode == modeReview {
			return m.updateReview(msg)
		}
		switch msg.Type {
		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit

		case tea.KeyUp:
			if len(m.history) > 0 {
				if m.histIdx == -1 {
					m.histIdx = len(m.history) - 1
				} else if m.histIdx > 0 {
					m.histIdx--
				}
				m.textInput.SetValue(m.history[m.histIdx])
				m.textInput.CursorEnd()
			}
			return m, nil

		case tea.KeyDown:
			if m.histIdx != -1 {
				if m.histIdx < len(m.history)-1 {
					m.histIdx++
					m.textInput.SetValue(m.history[m.histIdx])
					m.textInput.CursorEnd()
				} else {
					m.histIdx = -1
					m.textInput.SetValue("")
				}
			}
			return m, nil

		case tea.KeyEnter:
			input := strings.TrimSpace(m.textInput.Value())
			m.textInput.SetValue("")
			m.histIdx = -1

			if input == "" {
				return m, nil
			}

			// Add to history (dedup consecutive)
			if len(m.history) == 0 || m.history[len(m.history)-1] != input {
				m.history = append(m.history, input)
			}

			// Styled echo of what was typed
			echoLine := lipgloss.NewStyle().Foreground(ui.Cyan).Render("› ") +
				lipgloss.NewStyle().Foreground(ui.White).Render(input)
			m.output = append(m.output, echoLine)

			// Handle exit/quit
			if input == "exit" || input == "quit" {
				m.quitting = true
				return m, tea.Quit
			}

			// Handle help
			if input == "help" {
				m.output = append(m.output, renderHelp())
				return m, nil
			}

			// Handle clear
			if input == "clear" {
				m.output = nil
				return m, nil
			}

			// Handle interactive review: analyze the cluster, then step through
			// the proposed actions inline. Runs in this same program (no nested
			// TUI), which is why it lives here instead of as a cobra command.
			fields := strings.Fields(input)
			if fields[0] == "review" {
				m.output = append(m.output, ui.DimStyle.Render("  Analyzing cluster..."))
				return m, startReview(fields[1:])
			}

			// Execute subcommand and refresh status
			result := executeCommand(m.rootCmd, input)
			if result != "" {
				m.output = append(m.output, result)
			}
			return m, checkConnStatus
		}
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

// ── Interactive review (in-REPL approval flow) ──────────────────────────────

// reviewLoadedMsg carries the result of analyzing the cluster for review.
type reviewLoadedMsg struct {
	decisions []types.Decision
	exec      executor.Executor
	label     string
	err       error
}

// startReview analyzes the cluster off the UI thread and hands the proposed
// decisions back as a message. For safety the REPL never mutates a real cluster:
// mock metrics get a simulated executor, while k8s falls back to dry-run. Real
// changes are reserved for `analyze --apply` and the monitor, which ask for it
// explicitly.
func startReview(args []string) tea.Cmd {
	metricsSource := "mock"
	namespace := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--metrics":
			if i+1 < len(args) {
				metricsSource = args[i+1]
				i++
			}
		case "--namespace":
			if i+1 < len(args) {
				namespace = args[i+1]
				i++
			}
		}
	}

	return func() tea.Msg {
		provider, err := makeProvider(metricsSource, namespace)
		if err != nil {
			return reviewLoadedMsg{err: err}
		}
		engine := decision.NewFallbackEngine(
			llm.NewEngine(llm.NewClient("http://localhost:11434", "llama3")),
			decision.NewRuleBasedEngine(),
		)
		snapshot, err := provider.GetClusterSnapshot()
		if err != nil {
			return reviewLoadedMsg{err: err}
		}
		decisions, err := engine.Analyze(snapshot)
		if err != nil {
			return reviewLoadedMsg{err: err}
		}
		// The REPL review flow is a sandbox (k8s falls back to dry-run), so its
		// executions never mutate a real cluster and are not audited.
		exec, label, err := buildExecutor(metricsSource, metricsSource == "k8s", audit.NewNopSink())
		if err != nil {
			return reviewLoadedMsg{err: err}
		}
		return reviewLoadedMsg{decisions: decisions, exec: exec, label: label}
	}
}

func (m replModel) onReviewLoaded(msg reviewLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.output = append(m.output, errorStyle.Render("  Error: "+msg.err.Error()))
		return m, nil
	}
	session := newReviewSession(msg.decisions)
	if session.total() == 0 {
		m.output = append(m.output, ui.DimStyle.Render(
			fmt.Sprintf("  Nothing to review — %d decision(s), none actionable.", len(msg.decisions))))
		return m, nil
	}
	m.mode = modeReview
	m.session = session
	m.exec = msg.exec
	m.reviewAll = false
	m.auto = false
	m.working = false
	m.output = append(m.output, ui.DimStyle.Render("  Executor: "+msg.label))
	return m, nil
}

func (m replModel) updateReview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.working {
		return m, nil // ignore input while an action runs
	}
	switch msg.String() {
	case "tab":
		// Toggle sticky auto-accept. Turning it on runs the next reversible
		// action immediately; on a non-reversible one it just waits.
		m.auto = !m.auto
		return m.autoAdvance()
	case "a", "y":
		m.working = true
		return m, runExecutor(m.exec, m.session.current())
	case "A":
		m.reviewAll = true
		m.working = true
		return m, runExecutor(m.exec, m.session.current())
	case "r", "n":
		m = m.recordAndLog(types.StatusRejected, "")
		if m.session.done() {
			return m.exitReview(false), nil
		}
		// Resume auto mode on the next item if the operator cleared a
		// non-reversible one that had paused the queue.
		return m.autoAdvance()
	case "s":
		m = m.recordAndLog(statusSkipped, "")
		if m.session.done() {
			return m.exitReview(false), nil
		}
		return m.autoAdvance()
	case "q", "esc":
		m = m.exitReview(true)
		return m, nil
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m replModel) onExecDone(msg execDoneMsg) (tea.Model, tea.Cmd) {
	errStr := ""
	if msg.err != nil {
		errStr = msg.err.Error()
	}
	m = m.recordAndLog(msg.status, errStr)
	m.working = false
	// A failure pauses "approve all" and auto mode so the operator can react.
	if msg.status == types.StatusFailed {
		m.reviewAll = false
		m.auto = false
	}
	if m.session.done() {
		return m.exitReview(false), nil
	}
	if m.reviewAll {
		m.working = true
		return m, runExecutor(m.exec, m.session.current())
	}
	return m.autoAdvance()
}

func (m replModel) recordAndLog(status types.DecisionStatus, errStr string) replModel {
	m.session.record(status, errStr)
	m.output = append(m.output, renderOutcome(m.session.outcomes[len(m.session.outcomes)-1]))
	return m
}

func (m replModel) exitReview(aborted bool) replModel {
	if aborted {
		m.output = append(m.output, ui.DimStyle.Render("  Review cancelled."))
	} else {
		m.output = append(m.output, renderSummary(m.session))
	}
	m.mode = modeInput
	m.session = nil
	m.exec = nil
	m.reviewAll = false
	m.auto = false
	m.working = false
	return m
}

func (m replModel) View() string {
	if m.quitting {
		return goodbyeStyle.Render("  Goodbye!") + "\n"
	}

	var b strings.Builder

	// ── Output (prints inline, scrolls naturally) ─────────────────────────
	if len(m.output) > 0 {
		b.WriteString(strings.Join(m.output, "\n"))
		b.WriteString("\n")
	}

	// ── Review panel (replaces the prompt while reviewing) ────────────────
	if m.mode == modeReview && m.session != nil && !m.session.done() {
		b.WriteString("\n")
		b.WriteString(renderDecisionPrompt(m.session.current(), m.session.idx+1, m.session.total()))
		if m.working {
			b.WriteString(ui.YellowStyle.Render("  executing...") + "\n")
		} else {
			if notice := autoNotice(m.auto, m.session.current()); notice != "" {
				b.WriteString(notice + "\n")
			}
			b.WriteString(reviewHint(m.auto) + "\n")
		}
		return b.String()
	}

	// ── Input box (always at the bottom) ──────────────────────────────────
	boxWidth := m.width - 2
	if boxWidth < 30 {
		boxWidth = 30
	}

	label := inputLabelStyle.Render(" CourtVision ")
	inputContent := m.textInput.View()

	box := inputBoxStyle.
		Width(boxWidth).
		Render(inputContent)

	// Overlay the label on the top border
	boxLines := strings.Split(box, "\n")
	if len(boxLines) > 0 {
		topBorder := boxLines[0]
		runes := []rune(topBorder)
		labelRendered := label
		labelWidth := lipgloss.Width(labelRendered)
		if len(runes) > labelWidth+4 {
			boxLines[0] = string(runes[:2]) + labelRendered + string(runes[2+labelWidth:])
		}
		box = strings.Join(boxLines, "\n")
	}

	b.WriteString(box)

	return b.String()
}

// ── Help renderer ─────────────────────────────────────────────────────────────

func renderHelp() string {
	var b strings.Builder
	headerStyle := lipgloss.NewStyle().
		Foreground(ui.Purple).
		Bold(true)

	nameStyle := lipgloss.NewStyle().
		Foreground(ui.Cyan).
		Bold(true).
		Width(10)

	descStyle := lipgloss.NewStyle().
		Foreground(ui.Gray)

	b.WriteString(headerStyle.Render("  Available Commands:"))
	b.WriteString("\n")

	commands := []struct{ name, desc string }{
		{"monitor", "Start the monitoring agent"},
		{"analyze", "Run a one-shot cluster analysis"},
		{"review", "Analyze, then approve/reject each action inline"},
		{"status", "Check connectivity to Ollama and Kubernetes"},
		{"version", "Print version information"},
		{"clear", "Clear output"},
		{"help", "Show this help message"},
		{"exit", "Exit the REPL (also: quit, Ctrl+C)"},
	}

	for _, c := range commands {
		b.WriteString(fmt.Sprintf("    %s %s\n",
			nameStyle.Render(c.name),
			descStyle.Render(c.desc),
		))
	}

	b.WriteString(ui.DimStyle.Render("  Tip: ↑/↓ arrows cycle through command history"))
	return b.String()
}

// ── Command executor ──────────────────────────────────────────────────────────

func executeCommand(rootCmd *cobra.Command, input string) string {
	args := strings.Fields(input)
	if len(args) == 0 {
		return ""
	}

	// Check if the command exists
	cmd, _, err := rootCmd.Find(args)
	if err != nil || cmd == rootCmd {
		return errorStyle.Render(fmt.Sprintf("  Unknown command: %s", args[0])) +
			"\n" + ui.DimStyle.Render("  Type \"help\" to see available commands")
	}

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	rootCmd.SetArgs(args)
	execErr := rootCmd.Execute()

	w.Close()
	os.Stdout = old

	outputBytes, _ := io.ReadAll(r)
	r.Close()

	output := string(outputBytes)
	if execErr != nil {
		return errorStyle.Render(fmt.Sprintf("  Error: %v", execErr))
	}
	return strings.TrimRight(output, "\n")
}

// ── Startup banner (printed once before TUI) ─────────────────────────────────

func printStartupBanner() {
	fmt.Println(ui.Banner())
	fmt.Println(ui.SubtitleStyle.Render("  Agentic Infrastructure Monitor"))
	fmt.Println(ui.DimStyle.Render(fmt.Sprintf("  %s (commit: %s)", version, commit)))
	fmt.Println()

	// Quick connectivity check
	s := checkConnStatus().(statusMsg)

	ollamaDot := statusDotRed
	ollamaLabel := "disconnected"
	if s.ollamaOK {
		ollamaDot = statusDotGreen
		ollamaLabel = "connected"
	}

	k8sDot := statusDotRed
	k8sLabel := "disconnected"
	if s.k8sOK {
		k8sDot = statusDotGreen
		k8sLabel = "connected"
	}

	fmt.Printf(" %s Ollama %s   %s Kubernetes %s\n",
		ollamaDot, statusBarStyle.Render(ollamaLabel),
		k8sDot, statusBarStyle.Render(k8sLabel))
	fmt.Println()
	fmt.Println(ui.DimStyle.Render("  Type \"help\" for commands, \"exit\" to quit"))
	fmt.Println()
}

// ── Entry point ───────────────────────────────────────────────────────────────

func runREPL(rootCmd *cobra.Command) {
	printStartupBanner()

	p := tea.NewProgram(
		newREPL(rootCmd),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running REPL: %v\n", err)
		os.Exit(1)
	}
}
