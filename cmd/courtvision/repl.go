package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/atulya-singh/CourtVision/internal/ui"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	promptStyle = lipgloss.NewStyle().
			Foreground(ui.Cyan).
			Bold(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(ui.Red)

	successStyle = lipgloss.NewStyle().
			Foreground(ui.Green)
)

type replModel struct {
	textInput textinput.Model
	rootCmd   *cobra.Command
	history   []string
	histIdx   int
	output    string
	quitting  bool
}

func newREPL(rootCmd *cobra.Command) replModel {
	ti := textinput.New()
	ti.Prompt = promptStyle.Render("› ")
	ti.Focus()
	ti.CharLimit = 256
	ti.Width = 80

	return replModel{
		textInput: ti,
		rootCmd:   rootCmd,
		history:   []string{},
		histIdx:   -1,
	}
}

func (m replModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m replModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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
				m.output = ""
				return m, nil
			}

			// Add to history
			if len(m.history) == 0 || m.history[len(m.history)-1] != input {
				m.history = append(m.history, input)
			}

			// Handle exit/quit
			if input == "exit" || input == "quit" {
				m.quitting = true
				m.output = successStyle.Render("Goodbye!")
				return m, tea.Quit
			}

			// Handle help
			if input == "help" {
				m.output = renderHelp()
				return m, nil
			}

			// Handle clear
			if input == "clear" {
				m.output = ""
				return m, tea.ClearScreen
			}

			// Execute as cobra subcommand
			m.output = executeCommand(m.rootCmd, input)
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m replModel) View() string {
	if m.quitting {
		if m.output != "" {
			return m.output + "\n"
		}
		return ""
	}

	var b strings.Builder
	if m.output != "" {
		b.WriteString(m.output)
		b.WriteString("\n\n")
	}
	b.WriteString(m.textInput.View())
	return b.String()
}

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
	b.WriteString("\n\n")

	commands := []struct{ name, desc string }{
		{"monitor", "Start the monitoring agent with API server and dashboard"},
		{"analyze", "Run a one-shot cluster analysis and print results"},
		{"status", "Check connectivity to Ollama and Kubernetes"},
		{"version", "Print version information"},
		{"clear", "Clear the screen"},
		{"help", "Show this help message"},
		{"exit", "Exit the REPL (also: quit, Ctrl+C)"},
	}

	for _, c := range commands {
		b.WriteString(fmt.Sprintf("    %s %s\n",
			nameStyle.Render(c.name),
			descStyle.Render(c.desc),
		))
	}

	b.WriteString("\n")
	b.WriteString(ui.DimStyle.Render("  Tip: Use ↑/↓ arrows to cycle through command history"))
	return b.String()
}

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

	// Capture stdout by redirecting cobra output
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

func runREPL(rootCmd *cobra.Command) {
	// Show banner
	fmt.Println(ui.Banner())
	fmt.Println()
	fmt.Println(ui.SubtitleStyle.Render("  Agentic Infrastructure Monitor"))
	fmt.Println(ui.DimStyle.Render(fmt.Sprintf("  %s (commit: %s)", version, commit)))
	fmt.Println()
	fmt.Println(ui.DimStyle.Render("  Interactive mode — type \"help\" for commands, \"exit\" to quit"))
	fmt.Println()

	p := tea.NewProgram(newREPL(rootCmd))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running REPL: %v\n", err)
		os.Exit(1)
	}
}
