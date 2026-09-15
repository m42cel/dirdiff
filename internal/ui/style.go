package ui

import "github.com/charmbracelet/lipgloss"

// Colors are chosen so every status also has a distinct glyph (SPEC.md
// §6) — color is never the only signal.
var (
	paneStyle = lipgloss.NewStyle().Padding(0, 1)

	headerStyle = lipgloss.NewStyle().Bold(true).Underline(true)

	cursorStyle = lipgloss.NewStyle().Reverse(true)

	sameStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	differsStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	rollupSameStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Faint(true)
	missingStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errorStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	pendingStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	dimStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	placeholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)

	statusBarStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	detailsStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("7")).BorderStyle(lipgloss.NormalBorder()).BorderTop(true)
)
