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

	// session modes: the operational context the next command runs under, set
	// once with /metrics or /namespace instead of re-typed as flags each time.
	metrics   string // "mock" | "k8s"
	namespace string // "" = all namespaces

	// slash-command palette (the "/…" dropdown). Open while the input starts
	// with "/"; paletteItems is the current filtered set, paletteIdx the highlight.
	paletteOpen  bool
	paletteItems []slashCommand
	paletteIdx   int

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
		metrics:   "mock",
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

		case tea.KeyShiftTab:
			// Quick mode switch: cycle metrics mock<->k8s, mirroring Claude Code's
			// Shift+Tab mode toggle.
			return m.toggleMetrics(), nil

		case tea.KeyEsc:
			m.paletteOpen = false
			return m, nil

		case tea.KeyTab:
			// Complete the highlighted palette entry into the input line.
			if m.paletteOpen && len(m.paletteItems) > 0 {
				return m.completeSelected(), nil
			}
			return m, nil

		case tea.KeyUp:
			// While the palette is open ↑/↓ move the highlight; otherwise they
			// cycle command history exactly as before.
			if m.paletteOpen {
				if m.paletteIdx > 0 {
					m.paletteIdx--
				}
				return m, nil
			}
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
			if m.paletteOpen {
				if m.paletteIdx < len(m.paletteItems)-1 {
					m.paletteIdx++
				}
				return m, nil
			}
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
			// With the palette open, Enter runs the highlighted command (keeping
			// any args the operator already typed after the command word).
			if m.paletteOpen && len(m.paletteItems) > 0 {
				input = m.paletteChoice(input)
			}
			m.textInput.SetValue("")
			m.histIdx = -1
			m.paletteOpen = false

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

			return m.dispatch(input)

		default:
			// Any other key edits the input; refresh the palette from the new value.
			var cmd tea.Cmd
			m.textInput, cmd = m.textInput.Update(msg)
			return m.refreshPalette(), cmd
		}
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

// ── Slash-command dispatch + palette helpers ────────────────────────────────

// dispatch routes one submitted line (typed or picked from the palette) to the
// right handler. A leading "/" is optional and bare words still resolve, so old
// history entries and muscle memory keep working. It returns the updated model
// and any command to run.
func (m replModel) dispatch(raw string) (replModel, tea.Cmd) {
	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(raw), "/"))
	if len(fields) == 0 {
		return m, nil
	}
	name, rest := fields[0], fields[1:]

	c, ok := lookupSlash(name)
	if !ok {
		m.output = append(m.output,
			errorStyle.Render("  Unknown command: "+name)+"\n"+
				ui.DimStyle.Render("  Press / to open the command palette, or type help"))
		return m, nil
	}

	switch c.kind {
	case metaExit:
		m.quitting = true
		return m, tea.Quit
	case metaHelp:
		m.output = append(m.output, renderHelp())
		return m, nil
	case metaClear:
		m.output = nil
		return m, nil
	case setMetrics:
		return m.applyMetrics(rest), nil
	case setNamespace:
		return m.applyNamespace(rest), nil
	case inlineReview:
		// Analyze the cluster, then step through the proposed actions inline.
		// Runs in this same program (no nested TUI), using the session mode.
		m.output = append(m.output, ui.DimStyle.Render("  Analyzing cluster..."))
		return m, startReview(m.metrics, m.namespace)
	case externalCmd:
		m.output = append(m.output, externalHint(c, m.metrics, m.namespace))
		return m, nil
	default: // cobraCmd
		result := executeCommand(m.rootCmd, cobraArgsFor(c, m.metrics, m.namespace, rest))
		if result != "" {
			m.output = append(m.output, result)
		}
		return m, checkConnStatus
	}
}

// refreshPalette recomputes the dropdown from the current input value: open and
// filtered while the line starts with "/", closed otherwise.
func (m replModel) refreshPalette() replModel {
	val := strings.TrimSpace(m.textInput.Value())
	if !strings.HasPrefix(val, "/") {
		m.paletteOpen = false
		m.paletteItems = nil
		m.paletteIdx = 0
		return m
	}
	if !m.paletteOpen {
		m.paletteIdx = 0 // fresh open starts at the top
	}
	m.paletteItems = matchSlash(val)
	m.paletteOpen = true
	if m.paletteIdx >= len(m.paletteItems) {
		m.paletteIdx = len(m.paletteItems) - 1
	}
	if m.paletteIdx < 0 {
		m.paletteIdx = 0
	}
	return m
}

// completeSelected fills the input with the highlighted command, leaving a
// trailing space when it takes arguments so the operator can keep typing.
func (m replModel) completeSelected() replModel {
	c := m.paletteItems[m.paletteIdx]
	val := "/" + c.name
	if c.args != "" {
		val += " "
	}
	m.textInput.SetValue(val)
	m.textInput.CursorEnd()
	return m.refreshPalette()
}

// paletteChoice resolves what Enter should run when the palette is open: the
// highlighted command, but preserving any args already typed after the word.
func (m replModel) paletteChoice(input string) string {
	c := m.paletteItems[m.paletteIdx]
	fields := strings.Fields(strings.TrimPrefix(input, "/"))
	if len(fields) >= 2 {
		return "/" + c.name + " " + strings.Join(fields[1:], " ")
	}
	return "/" + c.name
}

// ── Session mode switching ──────────────────────────────────────────────────

func (m replModel) toggleMetrics() replModel {
	if m.metrics == "k8s" {
		m.metrics = "mock"
	} else {
		m.metrics = "k8s"
	}
	m.output = append(m.output, ui.GreenStyle.Render("  metrics → "+m.metrics)+
		ui.DimStyle.Render("  (Shift+Tab to toggle)"))
	return m
}

func (m replModel) applyMetrics(rest []string) replModel {
	if len(rest) == 0 {
		m.output = append(m.output, ui.DimStyle.Render("  metrics is "+m.metrics+"  (usage: /metrics mock|k8s)"))
		return m
	}
	v := strings.ToLower(rest[0])
	if v != "mock" && v != "k8s" {
		m.output = append(m.output, errorStyle.Render("  metrics must be 'mock' or 'k8s'"))
		return m
	}
	m.metrics = v
	m.output = append(m.output, ui.GreenStyle.Render("  metrics → "+v))
	return m
}

func (m replModel) applyNamespace(rest []string) replModel {
	if len(rest) == 0 || strings.EqualFold(rest[0], "all") {
		m.namespace = ""
		m.output = append(m.output, ui.GreenStyle.Render("  namespace → all"))
		return m
	}
	m.namespace = rest[0]
	m.output = append(m.output, ui.GreenStyle.Render("  namespace → "+rest[0]))
	return m
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
// decisions back as a message, using the session's metrics/namespace mode. For
// safety the REPL never mutates a real cluster: mock metrics get a simulated
// executor, while k8s falls back to dry-run. Real changes are reserved for
// `analyze --apply` and the monitor, which ask for it explicitly.
func startReview(metricsSource, namespace string) tea.Cmd {
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

	// ── Command palette (dropdown while typing "/…") ──────────────────────
	if m.paletteOpen {
		b.WriteString(renderPalette(m.paletteItems, m.paletteIdx))
		b.WriteString("\n")
	}

	// ── Mode bar (current session context, above the prompt) ──────────────
	b.WriteString(m.renderModeBar())
	b.WriteString("\n")

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

// renderHelp lists every command from the shared registry, so it can never drift
// from the palette.
func renderHelp() string {
	var b strings.Builder
	headerStyle := lipgloss.NewStyle().
		Foreground(ui.Purple).
		Bold(true)

	nameStyle := lipgloss.NewStyle().
		Foreground(ui.Cyan).
		Bold(true).
		Width(22)

	descStyle := lipgloss.NewStyle().
		Foreground(ui.Gray)

	b.WriteString(headerStyle.Render("  Commands ") + ui.DimStyle.Render("(type / to open the palette)"))
	b.WriteString("\n")

	for _, c := range slashCommands() {
		b.WriteString(fmt.Sprintf("    %s %s\n",
			nameStyle.Render(slashLabel(c)),
			descStyle.Render(c.desc),
		))
	}

	b.WriteString(ui.DimStyle.Render("  ↑/↓ history · Shift+Tab toggle metrics · Tab complete · Esc dismiss"))
	return b.String()
}

// renderModeBar shows the live session context — connectivity plus the metrics
// source and namespace filter the next command will run under — so the current
// "mode" is always visible above the prompt.
func (m replModel) renderModeBar() string {
	ollamaDot := statusDotRed
	if m.status.ollamaOK {
		ollamaDot = statusDotGreen
	}
	ns := m.namespace
	if ns == "" {
		ns = "all"
	}
	sep := statusBarStyle.Render(" · ")
	return "  " + ollamaDot + statusBarStyle.Render(" ollama") + sep +
		statusBarStyle.Render("metrics:") + ui.CyanStyle.Render(m.metrics) + sep +
		statusBarStyle.Render("ns:") + ui.CyanStyle.Render(ns) + sep +
		statusBarStyle.Render("sandbox")
}

// ── Command executor ──────────────────────────────────────────────────────────

func executeCommand(rootCmd *cobra.Command, args []string) string {
	if len(args) == 0 {
		return ""
	}

	// Check if the command exists
	cmd, _, err := rootCmd.Find(args)
	if err != nil || cmd == rootCmd {
		return errorStyle.Render(fmt.Sprintf("  Unknown command: %s", args[0])) +
			"\n" + ui.DimStyle.Render("  Press / to see available commands")
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
	fmt.Println(ui.DimStyle.Render("  Press / for the command palette · \"help\" for a list · \"exit\" to quit"))
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
