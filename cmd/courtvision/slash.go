package main

import (
	"fmt"
	"strings"

	"github.com/atulya-singh/CourtVision/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

// cmdKind classifies how the REPL should carry out a slash command. Keeping the
// routing on the registry (rather than a switch scattered through Update) is what
// lets the palette, the dispatcher, and `help` share one source of truth.
type cmdKind int

const (
	cobraCmd     cmdKind = iota // delegate to the Cobra command tree (analyze, status, version)
	inlineReview                // the in-REPL approve/reject flow
	externalCmd                 // long-lived server: print a shell hint instead of blocking the REPL
	setMetrics                  // switch the session metrics source
	setNamespace                // set the session namespace filter
	metaHelp
	metaClear
	metaExit
)

// slashCommand is one entry in the REPL command palette. It is the single source
// of truth shared by the palette dropdown, the dispatcher, and `help`, so the
// three can never drift apart.
type slashCommand struct {
	name    string   // shown (and typed) as "/name"
	aliases []string // extra names that resolve to this command, e.g. "quit"
	args    string   // argument hint shown in the palette, e.g. "<mock|k8s>"
	desc    string
	kind    cmdKind
}

// slashCommands is the registry. Order is the order shown in the palette and in
// `help`.
func slashCommands() []slashCommand {
	return []slashCommand{
		{name: "analyze", desc: "Run a one-shot cluster analysis", kind: cobraCmd},
		{name: "review", desc: "Analyze, then approve/reject each action inline", kind: inlineReview},
		{name: "monitor", desc: "Start the single-cluster monitoring server (shell)", kind: externalCmd},
		{name: "multi-monitor", desc: "Start the multi-cluster fleet server (shell)", kind: externalCmd},
		{name: "status", desc: "Check connectivity to Ollama and Kubernetes", kind: cobraCmd},
		{name: "metrics", args: "<mock|k8s>", desc: "Switch the metrics source for this session", kind: setMetrics},
		{name: "namespace", args: "<ns|all>", desc: "Set the namespace filter for this session", kind: setNamespace},
		{name: "version", desc: "Print version information", kind: cobraCmd},
		{name: "clear", desc: "Clear the output", kind: metaClear},
		{name: "help", desc: "Show available commands", kind: metaHelp},
		{name: "exit", aliases: []string{"quit"}, desc: "Exit the REPL (also: Ctrl+C)", kind: metaExit},
	}
}

// matchSlash returns the registry entries whose name or an alias has the typed
// token (the text after "/", up to the first space) as a case-insensitive prefix.
// An empty token (a bare "/") matches everything, so typing "/" opens the full
// menu.
func matchSlash(input string) []slashCommand {
	token := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(input), "/"))
	if i := strings.IndexByte(token, ' '); i >= 0 {
		token = token[:i] // only the command word filters; ignore any args typed
	}
	var out []slashCommand
	for _, c := range slashCommands() {
		if strings.HasPrefix(c.name, token) || aliasHasPrefix(c, token) {
			out = append(out, c)
		}
	}
	return out
}

func aliasHasPrefix(c slashCommand, token string) bool {
	for _, a := range c.aliases {
		if strings.HasPrefix(a, token) {
			return true
		}
	}
	return false
}

// lookupSlash resolves a bare command word (no leading slash) to its registry
// entry by exact name or alias, case-insensitively.
func lookupSlash(name string) (slashCommand, bool) {
	name = strings.ToLower(name)
	for _, c := range slashCommands() {
		if c.name == name {
			return c, true
		}
		for _, a := range c.aliases {
			if a == name {
				return c, true
			}
		}
	}
	return slashCommand{}, false
}

// cobraArgsFor turns a cobra-backed slash command plus the current session mode
// into the argument list the Cobra command expects, so switching modes with
// /metrics or /namespace carries into the command without re-typing flags. Only
// commands that actually declare --metrics/--namespace get them (status/version
// do not).
func cobraArgsFor(c slashCommand, metrics, namespace string, extra []string) []string {
	args := []string{c.name}
	if c.name == "analyze" {
		args = append(args, "--metrics", metrics)
		if namespace != "" && !strings.EqualFold(namespace, "all") {
			args = append(args, "--namespace", namespace)
		}
	}
	return append(args, extra...)
}

// externalHint renders the shell command to start a long-lived server, built
// from the current session mode. Running these in-process would block the REPL's
// event loop, so we point the operator at their shell instead.
func externalHint(c slashCommand, metrics, namespace string) string {
	cmd := "courtvision " + c.name
	if c.name == "multi-monitor" {
		cmd += " --clusters <ctx1,ctx2>"
	}
	cmd += " --metrics " + metrics
	if namespace != "" && !strings.EqualFold(namespace, "all") {
		cmd += " --namespace " + namespace
	}
	return ui.DimStyle.Render("  "+c.name+" is a long-running server — start it from your shell:") +
		"\n    " + ui.CyanStyle.Render(cmd)
}

// ── Palette rendering ──────────────────────────────────────────────────────────

var paletteSelStyle = lipgloss.NewStyle().
	Foreground(ui.Purple).
	Bold(true)

// renderPalette draws the command dropdown with the selected row highlighted,
// windowing to at most maxRows so a long list never overruns the prompt.
func renderPalette(items []slashCommand, sel int) string {
	if len(items) == 0 {
		return ui.DimStyle.Render("  (no matching command — Esc to dismiss)")
	}

	const maxRows = 8
	start := 0
	if len(items) > maxRows && sel >= maxRows {
		start = sel - maxRows + 1
	}
	end := start + maxRows
	if end > len(items) {
		end = len(items)
	}

	// Width of the "/name <args>" column, so descriptions line up.
	nameW := 0
	for _, c := range items {
		if l := len(slashLabel(c)); l > nameW {
			nameW = l
		}
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		c := items[i]
		row := fmt.Sprintf("%-*s  %s", nameW, slashLabel(c), c.desc)
		if i == sel {
			b.WriteString("  " + paletteSelStyle.Render("▸ "+row))
		} else {
			b.WriteString("    " + ui.DimStyle.Render(row))
		}
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// slashLabel is the "/name <args>" form shown in the palette and help.
func slashLabel(c slashCommand) string {
	label := "/" + c.name
	if c.args != "" {
		label += " " + c.args
	}
	return label
}
