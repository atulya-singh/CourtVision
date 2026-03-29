package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── Color palette ──────────────────────────────────────────────────────────

var (
	Purple  = lipgloss.Color("#7C3AED")
	Cyan    = lipgloss.Color("#06B6D4")
	Blue    = lipgloss.Color("#3B82F6")
	Green   = lipgloss.Color("#22C55E")
	Yellow  = lipgloss.Color("#EAB308")
	Orange  = lipgloss.Color("#F97316")
	Red     = lipgloss.Color("#EF4444")
	Gray    = lipgloss.Color("#6B7280")
	DimGray = lipgloss.Color("#4B5563")
	White   = lipgloss.Color("#F9FAFB")
)

// ── Reusable text styles ───────────────────────────────────────────────────

var (
	BrandStyle    = lipgloss.NewStyle().Foreground(Purple).Bold(true)
	SubtitleStyle = lipgloss.NewStyle().Foreground(Gray)
	DimStyle      = lipgloss.NewStyle().Foreground(DimGray)
	CyanStyle     = lipgloss.NewStyle().Foreground(Cyan)
	BlueStyle     = lipgloss.NewStyle().Foreground(Blue)
	GreenStyle    = lipgloss.NewStyle().Foreground(Green)
	YellowStyle   = lipgloss.NewStyle().Foreground(Yellow)
	OrangeStyle   = lipgloss.NewStyle().Foreground(Orange)
	RedStyle      = lipgloss.NewStyle().Foreground(Red)
	WhiteStyle    = lipgloss.NewStyle().Foreground(White)
	BoldStyle     = lipgloss.NewStyle().Bold(true)
)

// ── Severity badge styles ──────────────────────────────────────────────────

var (
	CriticalBadge = lipgloss.NewStyle().
			Background(Red).
			Foreground(White).
			Bold(true).
			Padding(0, 1)

	HighBadge = lipgloss.NewStyle().
			Background(Orange).
			Foreground(White).
			Bold(true).
			Padding(0, 1)

	MediumBadge = lipgloss.NewStyle().
			Background(Yellow).
			Foreground(lipgloss.Color("#1F2937")).
			Bold(true).
			Padding(0, 1)

	LowBadge = lipgloss.NewStyle().
			Background(Green).
			Foreground(White).
			Bold(true).
			Padding(0, 1)
)

// SeverityBadge returns a styled severity string.
func SeverityBadge(severity string) string {
	s := strings.ToLower(severity)
	label := strings.ToUpper(severity)
	switch s {
	case "critical":
		return CriticalBadge.Render(label)
	case "high":
		return HighBadge.Render(label)
	case "medium":
		return MediumBadge.Render(label)
	case "low":
		return LowBadge.Render(label)
	default:
		return DimStyle.Render(label)
	}
}

// ── Status indicators ──────────────────────────────────────────────────────

var (
	CheckMark = GreenStyle.Render("✓")
	CrossMark = RedStyle.Render("✗")
	Dot       = DimStyle.Render("○")
)

// ── Box styles ─────────────────────────────────────────────────────────────

var (
	BorderBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Purple).
			Padding(1, 2)

	ConfigBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(DimGray).
			Padding(1, 2)
)

// ── ASCII banner ───────────────────────────────────────────────────────────

const bannerText = `
  ██████╗ ██████╗ ██╗   ██╗██████╗ ████████╗
 ██╔════╝██╔═══██╗██║   ██║██╔══██╗╚══██╔══╝
 ██║     ██║   ██║██║   ██║██████╔╝   ██║
 ██║     ██║   ██║██║   ██║██╔══██╗   ██║
 ╚██████╗╚██████╔╝╚██████╔╝██║  ██║   ██║
  ╚═════╝ ╚═════╝  ╚═════╝ ╚═╝  ╚═╝   ╚═╝
 ██╗   ██╗██╗███████╗██╗ ██████╗ ███╗   ██╗
 ██║   ██║██║██╔════╝██║██╔═══██╗████╗  ██║
 ██║   ██║██║███████╗██║██║   ██║██╔██╗ ██║
 ╚██╗ ██╔╝██║╚════██║██║██║   ██║██║╚██╗██║
  ╚████╔╝ ██║███████║██║╚██████╔╝██║ ╚████║
   ╚═══╝  ╚═╝╚══════╝╚═╝ ╚═════╝ ╚═╝  ╚═══╝`

func Banner() string {
	return BrandStyle.Render(bannerText)
}

// ── DryRun badge ───────────────────────────────────────────────────────────

var DryRunBadge = lipgloss.NewStyle().
	Background(Yellow).
	Foreground(lipgloss.Color("#1F2937")).
	Bold(true).
	Padding(0, 1).
	Render("DRY RUN")

// ── Config line helper ─────────────────────────────────────────────────────

func ConfigLine(label, value string) string {
	return fmt.Sprintf("  %s  %s", DimStyle.Render(fmt.Sprintf("%-12s", label)), WhiteStyle.Render(value))
}
