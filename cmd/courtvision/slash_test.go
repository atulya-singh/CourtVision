package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

func enterKey() tea.KeyMsg    { return tea.KeyMsg{Type: tea.KeyEnter} }
func escKey() tea.KeyMsg      { return tea.KeyMsg{Type: tea.KeyEsc} }
func upKey() tea.KeyMsg       { return tea.KeyMsg{Type: tea.KeyUp} }
func downKey() tea.KeyMsg     { return tea.KeyMsg{Type: tea.KeyDown} }
func shiftTabKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyShiftTab} }

// typed feeds a whole string into the REPL input as one key event, the way a
// paste (or a fast typist) would, then returns the resulting model.
func typed(t *testing.T, m replModel, s string) replModel {
	t.Helper()
	model, _ := m.Update(key(s))
	return model.(replModel)
}

func names(cs []slashCommand) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.name
	}
	return out
}

// ── Registry ──────────────────────────────────────────────────────────────────

func TestMatchSlash(t *testing.T) {
	if got := matchSlash("/an"); len(got) != 1 || got[0].name != "analyze" {
		t.Errorf("/an should match only analyze, got %v", names(got))
	}
	if got := matchSlash("/"); len(got) != len(slashCommands()) {
		t.Errorf("bare / should match every command, got %d/%d", len(got), len(slashCommands()))
	}
	if got := matchSlash("/AN"); len(got) != 1 || got[0].name != "analyze" {
		t.Errorf("matching should be case-insensitive, got %v", names(got))
	}
	if got := matchSlash("/qu"); len(got) != 1 || got[0].name != "exit" {
		t.Errorf("/qu should resolve via the quit alias to exit, got %v", names(got))
	}
	// A typed argument after the command word must not narrow the match away.
	if got := matchSlash("/metrics k8s"); len(got) != 1 || got[0].name != "metrics" {
		t.Errorf("args after the command should be ignored when filtering, got %v", names(got))
	}
}

func TestLookupSlash(t *testing.T) {
	if c, ok := lookupSlash("review"); !ok || c.kind != inlineReview {
		t.Errorf("review should resolve to the inline review command")
	}
	if c, ok := lookupSlash("quit"); !ok || c.name != "exit" {
		t.Errorf("quit alias should resolve to exit")
	}
	if _, ok := lookupSlash("nope"); ok {
		t.Errorf("an unknown command should not resolve")
	}
}

func TestSlashRegistryNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range slashCommands() {
		for _, n := range append([]string{c.name}, c.aliases...) {
			if seen[n] {
				t.Errorf("duplicate command/alias in registry: %s", n)
			}
			seen[n] = true
		}
	}
}

func TestRenderHelpListsEveryCommand(t *testing.T) {
	help := renderHelp()
	for _, c := range slashCommands() {
		if !strings.Contains(help, "/"+c.name) {
			t.Errorf("help output is missing /%s", c.name)
		}
	}
}

func TestCobraArgsFor(t *testing.T) {
	analyze, _ := lookupSlash("analyze")
	if got := strings.Join(cobraArgsFor(analyze, "k8s", "ns1", nil), " "); got != "analyze --metrics k8s --namespace ns1" {
		t.Errorf("analyze args = %q", got)
	}
	if got := strings.Join(cobraArgsFor(analyze, "mock", "all", nil), " "); got != "analyze --metrics mock" {
		t.Errorf("the 'all' namespace should be omitted, got %q", got)
	}
	status, _ := lookupSlash("status")
	if got := strings.Join(cobraArgsFor(status, "k8s", "ns1", nil), " "); got != "status" {
		t.Errorf("status declares no --metrics/--namespace, got %q", got)
	}
}

// ── Palette behaviour through Update ────────────────────────────────────────

func TestPaletteOpensAndFilters(t *testing.T) {
	m := typed(t, newREPL(&cobra.Command{}), "/")
	if !m.paletteOpen {
		t.Fatal("typing / should open the palette")
	}
	if len(m.paletteItems) != len(slashCommands()) {
		t.Errorf("bare / should list all commands, got %d", len(m.paletteItems))
	}

	m = typed(t, newREPL(&cobra.Command{}), "/an")
	if !m.paletteOpen || len(m.paletteItems) != 1 || m.paletteItems[0].name != "analyze" {
		t.Errorf("/an should filter to analyze, got %v", names(m.paletteItems))
	}
}

func TestPaletteNavigationClamps(t *testing.T) {
	m := typed(t, newREPL(&cobra.Command{}), "/")
	if m.paletteIdx != 0 {
		t.Fatalf("palette should start highlighted at 0, got %d", m.paletteIdx)
	}

	// Up at the top stays put.
	model, _ := m.Update(upKey())
	m = model.(replModel)
	if m.paletteIdx != 0 {
		t.Errorf("up at the top should clamp to 0, got %d", m.paletteIdx)
	}

	// Down advances.
	model, _ = m.Update(downKey())
	m = model.(replModel)
	if m.paletteIdx != 1 {
		t.Errorf("down should move to 1, got %d", m.paletteIdx)
	}

	// Down past the end clamps at the last item.
	for i := 0; i < len(slashCommands())+5; i++ {
		model, _ = m.Update(downKey())
		m = model.(replModel)
	}
	if want := len(slashCommands()) - 1; m.paletteIdx != want {
		t.Errorf("down should clamp to last index %d, got %d", want, m.paletteIdx)
	}
}

func TestPaletteTabCompletes(t *testing.T) {
	m := typed(t, newREPL(&cobra.Command{}), "/me")
	if len(m.paletteItems) != 1 || m.paletteItems[0].name != "metrics" {
		t.Fatalf("/me should filter to metrics, got %v", names(m.paletteItems))
	}
	model, _ := m.Update(tabKey())
	m = model.(replModel)
	if got := m.textInput.Value(); got != "/metrics " {
		t.Errorf("Tab should complete to '/metrics ' (args follow), got %q", got)
	}
}

func TestPaletteEscCloses(t *testing.T) {
	m := typed(t, newREPL(&cobra.Command{}), "/")
	model, _ := m.Update(escKey())
	m = model.(replModel)
	if m.paletteOpen {
		t.Error("Esc should close the palette")
	}
}

func TestPaletteChoicePreservesArgs(t *testing.T) {
	m := typed(t, newREPL(&cobra.Command{}), "/an")
	if got := m.paletteChoice("/an"); got != "/analyze" {
		t.Errorf("Enter on the highlighted item should run /analyze, got %q", got)
	}
	m = typed(t, newREPL(&cobra.Command{}), "/metrics k8s")
	if got := m.paletteChoice("/metrics k8s"); got != "/metrics k8s" {
		t.Errorf("already-typed args should be preserved, got %q", got)
	}
}

// TestHistoryStillWorksWithPaletteClosed guards the regression: ↑/↓ must keep
// cycling history when the palette is not open.
func TestHistoryStillWorksWithPaletteClosed(t *testing.T) {
	m := newREPL(&cobra.Command{})
	m.history = []string{"status", "version"}
	model, _ := m.Update(upKey())
	m = model.(replModel)
	if got := m.textInput.Value(); got != "version" {
		t.Errorf("up should recall the last history entry, got %q", got)
	}
}

// ── Session modes ───────────────────────────────────────────────────────────

func TestShiftTabTogglesMetrics(t *testing.T) {
	m := newREPL(&cobra.Command{})
	if m.metrics != "mock" {
		t.Fatalf("default metrics should be mock, got %s", m.metrics)
	}
	model, _ := m.Update(shiftTabKey())
	m = model.(replModel)
	if m.metrics != "k8s" {
		t.Errorf("Shift+Tab should switch to k8s, got %s", m.metrics)
	}
	model, _ = m.Update(shiftTabKey())
	m = model.(replModel)
	if m.metrics != "mock" {
		t.Errorf("Shift+Tab should switch back to mock, got %s", m.metrics)
	}
}

func TestMetricsAndNamespaceCommands(t *testing.T) {
	m := newREPL(&cobra.Command{})
	m = m.applyMetrics([]string{"k8s"})
	if m.metrics != "k8s" {
		t.Errorf("/metrics k8s should set k8s, got %s", m.metrics)
	}
	m = m.applyMetrics([]string{"bogus"})
	if m.metrics != "k8s" {
		t.Errorf("an invalid metrics value should be rejected (kept k8s), got %s", m.metrics)
	}
	m = m.applyNamespace([]string{"kube-system"})
	if m.namespace != "kube-system" {
		t.Errorf("/namespace kube-system should set the filter, got %q", m.namespace)
	}
	m = m.applyNamespace([]string{"all"})
	if m.namespace != "" {
		t.Errorf("/namespace all should clear the filter, got %q", m.namespace)
	}
}

// ── Dispatch ────────────────────────────────────────────────────────────────

func TestDispatchReviewParity(t *testing.T) {
	// Both "/review" and bare "review" must start the inline review flow.
	for _, in := range []string{"/review", "review"} {
		m, cmd := newREPL(&cobra.Command{}).dispatch(in)
		if cmd == nil {
			t.Errorf("dispatch(%q) should return a startReview command", in)
		}
		if !strings.Contains(strings.Join(m.output, "\n"), "Analyzing") {
			t.Errorf("dispatch(%q) should echo the analyzing notice", in)
		}
	}
}

func TestDispatchUnknown(t *testing.T) {
	m, _ := newREPL(&cobra.Command{}).dispatch("/nope")
	if !strings.Contains(strings.Join(m.output, "\n"), "Unknown command") {
		t.Errorf("an unknown command should report an error, got %v", m.output)
	}
}

func TestDispatchModeAndExit(t *testing.T) {
	m, _ := newREPL(&cobra.Command{}).dispatch("/metrics k8s")
	if m.metrics != "k8s" {
		t.Errorf("/metrics k8s via dispatch should set metrics, got %s", m.metrics)
	}
	m, cmd := m.dispatch("/exit")
	if !m.quitting || cmd == nil {
		t.Errorf("/exit should quit the REPL")
	}
}

func TestDispatchExternalHint(t *testing.T) {
	m, cmd := newREPL(&cobra.Command{}).dispatch("/monitor")
	if cmd != nil {
		t.Error("a long-running server must not run in-process from the REPL")
	}
	if !strings.Contains(strings.Join(m.output, "\n"), "courtvision monitor") {
		t.Errorf("/monitor should print a shell hint, got %v", m.output)
	}
}

// TestEnterRunsSelectedMetaCommand drives the full Update path: type "/cl",
// press Enter, and confirm the highlighted /clear ran (output cleared).
func TestEnterRunsSelectedMetaCommand(t *testing.T) {
	m := newREPL(&cobra.Command{})
	m.output = []string{"stale line"}
	m = typed(t, m, "/cl")
	if len(m.paletteItems) != 1 || m.paletteItems[0].name != "clear" {
		t.Fatalf("/cl should filter to clear, got %v", names(m.paletteItems))
	}
	model, _ := m.Update(enterKey())
	m = model.(replModel)
	if m.paletteOpen {
		t.Error("running a command should close the palette")
	}
	if len(m.output) != 0 {
		t.Errorf("/clear should have cleared the output, got %v", m.output)
	}
}
