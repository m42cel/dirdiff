package ui

import "github.com/charmbracelet/lipgloss"

// gutterWidth is the width of the single status-glyph column rendered
// between the two panes (SPEC.md §6 glyph, now centered instead of
// duplicated on each side).
const gutterWidth = 3

// Colors are chosen so every status also has a distinct glyph (SPEC.md
// §6) — color is never the only signal.
var (
	// paneStyle draws a full box around each pane, in the terminal's
	// default foreground color (no explicit color chosen — the two sides
	// aren't distinguished by color, only by position and the path title
	// above each box). No background color is used anywhere either — the
	// terminal's own background is unknown to us and painting over it
	// risks fighting the user's theme.
	paneStyle = lipgloss.NewStyle().Padding(0, 1).
			BorderStyle(lipgloss.NormalBorder())

	// titleStyle marks the path title, rendered in its own row above
	// each pane's box (see titleCellStyle in view.go) so the box's
	// border encloses only the entry list, not the path.
	titleStyle = lipgloss.NewStyle().Bold(true).Underline(true)

	gutterStyle = lipgloss.NewStyle().Width(gutterWidth).Align(lipgloss.Center)

	cursorStyle = lipgloss.NewStyle().Reverse(true)

	// Bright green (10), not the standard green (2) — plain green reads
	// too close to the not-yet-compared grey (8) in a lot of terminal
	// palettes, so "same" needs the extra punch to stay visually distinct
	// without going full neon.
	sameStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	differsStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	missingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	pendingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	// placeholderStyle adds Faint on top of the same grey dimStyle uses —
	// plain color 8 alone wasn't visually distinct enough from the
	// not-yet-compared glyph, which is also color 8. Faint (SGR 2, actual
	// reduced intensity) reads noticeably lighter than dimStyle's plain
	// grey, not just italicized.
	placeholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true).Italic(true)

	statusBarStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	detailsStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("7")).BorderStyle(lipgloss.NormalBorder()).BorderTop(true)

	// popupStyle frames the filter menu (SPEC.md §4.7) as a centered box,
	// distinct from helpView's full-screen overlay since the filter popup
	// is a small, transient selector rather than a reference screen.
	popupStyle = lipgloss.NewStyle().Padding(1, 2).BorderStyle(lipgloss.RoundedBorder())
)
