package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

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

type replModel struct {
	textInput  textinput.Model
	rootCmd    *cobra.Command
	history    []string
	histIdx    int
	outputLog  []string // accumulated command outputs
	status     connStatus
	width      int
	height     int
	quitting   bool
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
		height:    24,
	}
}

func (m replModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, checkConnStatus)
}

func (m replModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		boxInner := m.width - 4 // border + padding
		if boxInner < 20 {
			boxInner = 20
		}
		m.textInput.Width = boxInner - 4 // account for prompt chars
		return m, nil

	case statusMsg:
		m.status = connStatus(msg)
		return m, nil

	case tea.KeyMsg:
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
			m.outputLog = append(m.outputLog, echoLine)

			// Handle exit/quit
			if input == "exit" || input == "quit" {
				m.quitting = true
				return m, tea.Quit
			}

			// Handle help
			if input == "help" {
				m.outputLog = append(m.outputLog, renderHelp())
				return m, nil
			}

			// Handle clear
			if input == "clear" {
				m.outputLog = nil
				return m, nil
			}

			// Execute subcommand and refresh status
			result := executeCommand(m.rootCmd, input)
			if result != "" {
				m.outputLog = append(m.outputLog, result)
			}
			return m, checkConnStatus
		}
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m replModel) View() string {
	if m.quitting {
		return goodbyeStyle.Render("  Goodbye!") + "\n"
	}

	var sections []string

	// ── Banner area (top) ─────────────────────────────────────────────────
	banner := ui.Banner() + "\n" +
		ui.SubtitleStyle.Render("  Agentic Infrastructure Monitor") + "\n" +
		ui.DimStyle.Render(fmt.Sprintf("  %s (commit: %s)", version, commit))
	sections = append(sections, banner)

	// ── Status bar with horizontal line ───────────────────────────────────
	ollamaDot := statusDotRed
	ollamaLabel := "disconnected"
	if m.status.ollamaOK {
		ollamaDot = statusDotGreen
		ollamaLabel = "connected"
	}

	k8sDot := statusDotRed
	k8sLabel := "disconnected"
	if m.status.k8sOK {
		k8sDot = statusDotGreen
		k8sLabel = "connected"
	}

	statusText := fmt.Sprintf(" %s Ollama %s   %s Kubernetes %s ",
		ollamaDot, statusBarStyle.Render(ollamaLabel),
		k8sDot, statusBarStyle.Render(k8sLabel))

	lineWidth := m.width - lipgloss.Width(statusText) - 2
	if lineWidth < 0 {
		lineWidth = 0
	}
	line := lipgloss.NewStyle().Foreground(ui.DimGray).Render(strings.Repeat("─", lineWidth))
	statusBar := statusText + line
	sections = append(sections, statusBar)

	// ── Output area (scrollable) ──────────────────────────────────────────
	// Calculate how many lines we have for output
	// Total layout: banner(~14) + blank + statusbar(1) + blank + inputbox(3) + blank = ~20 fixed
	fixedLines := 20
	availableLines := m.height - fixedLines
	if availableLines < 3 {
		availableLines = 3
	}

	if len(m.outputLog) > 0 {
		allOutput := strings.Join(m.outputLog, "\n")
		outputLines := strings.Split(allOutput, "\n")

		// Scroll: only show the last N lines that fit
		if len(outputLines) > availableLines {
			outputLines = outputLines[len(outputLines)-availableLines:]
		}
		sections = append(sections, strings.Join(outputLines, "\n"))
	} else {
		hint := ui.DimStyle.Render("  Type \"help\" for commands, \"exit\" to quit")
		sections = append(sections, hint)
	}

	// ── Input box (pinned at bottom) ──────────────────────────────────────
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
		// Place label after the first 2 characters of the border
		labelRendered := label
		labelWidth := lipgloss.Width(labelRendered)
		if len(runes) > labelWidth+4 {
			// Insert label into the top border
			boxLines[0] = string(runes[:2]) + labelRendered + string(runes[2+labelWidth:])
		}
		box = strings.Join(boxLines, "\n")
	}

	sections = append(sections, box)

	return strings.Join(sections, "\n")
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
		Width(12)

	descStyle := lipgloss.NewStyle().
		Foreground(ui.Gray)

	b.WriteString(headerStyle.Render("  Available Commands:"))
	b.WriteString("\n")

	commands := []struct{ name, desc string }{
		{"monitor", "Start the monitoring agent"},
		{"analyze", "Run a one-shot cluster analysis"},
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

	var buf [64 * 1024]byte
	n, _ := r.Read(buf[:])
	r.Close()

	output := string(buf[:n])
	if execErr != nil {
		return errorStyle.Render(fmt.Sprintf("  Error: %v", execErr))
	}
	return strings.TrimRight(output, "\n")
}

// ── Entry point ───────────────────────────────────────────────────────────────

func runREPL(rootCmd *cobra.Command) {
	p := tea.NewProgram(
		newREPL(rootCmd),
		tea.WithAltScreen(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running REPL: %v\n", err)
		os.Exit(1)
	}
}
