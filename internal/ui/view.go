package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/session"
	"github.com/m42cel/dirdiff/internal/tree"
)

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	if m.showHelp {
		return helpView()
	}

	// Style.Width() already accounts for the style's own horizontal
	// padding, so the only extra columns a rendered pane box adds beyond
	// its Width are its two border columns — not border+padding, which
	// would double-count the padding and leave a gap on the right. When
	// avail is odd, the right pane takes the leftover column so the two
	// boxes still fill the terminal edge to edge instead of falling
	// short.
	const paneOverhead = 2
	avail := m.width - gutterWidth - 2*paneOverhead
	leftWidth := avail / 2
	if leftWidth < 8 {
		leftWidth = 8
	}
	rightWidth := leftWidth
	if remainder := avail - leftWidth*2; remainder > 0 {
		rightWidth += remainder
	}

	listHeight := m.listAreaHeight()

	// The path title sits above each pane's box (its own row, not the
	// box's first content line) so the box's border encloses only the
	// entry list. titleCellStyle's left padding (2) approximates the
	// box's own border(1)+padding(1) offset below it, so the title text
	// still lines up roughly over where row text starts inside the box;
	// its content width is trimmed by that same 2 columns, matching the
	// leftWidth/rightWidth truncate() calls below.
	titleCellStyle := lipgloss.NewStyle().PaddingLeft(2)
	leftTitle := titleStyle.Render(truncate(displayPath(m.sess.LeftRoot, m.cursorDir.RelPath), leftWidth))
	rightTitle := titleStyle.Render(truncate(displayPath(m.sess.RightRoot, m.cursorDir.RelPath), rightWidth))
	titleRow := lipgloss.JoinHorizontal(lipgloss.Top,
		titleCellStyle.Width(leftWidth+paneOverhead).Render(leftTitle),
		lipgloss.NewStyle().Width(gutterWidth).Render(""),
		titleCellStyle.Width(rightWidth+paneOverhead).Render(rightTitle),
	)

	leftRows, gutterRows, rightRows := m.renderPanes(listHeight)
	leftContent := strings.Join(leftRows, "\n")
	rightContent := strings.Join(rightRows, "\n")
	// The gutter has no border of its own, but the panes on either side
	// do — their top border line consumes one row that the gutter must
	// blank-pad for, or every glyph below renders one row too high.
	gutterContent := strings.Join(append([]string{""}, gutterRows...), "\n")

	body := lipgloss.JoinHorizontal(lipgloss.Top,
		paneStyle.Width(leftWidth).Height(listHeight).Render(leftContent),
		gutterStyle.Height(listHeight+1).Render(gutterContent),
		paneStyle.Width(rightWidth).Height(listHeight).Render(rightContent),
	)

	details := detailsStyle.Width(m.width).Height(detailsContentLines).Render(m.renderDetails())
	status := m.renderStatusBar()

	return titleRow + "\n" + body + "\n" + details + "\n" + status
}

// renderPanes returns the visible lines for both panes plus the status
// glyph gutter between them. A directory missing on one side renders as
// a single static placeholder on that side, with no rows and no gutter
// glyph (SPEC.md §4.3) — everything under a one-sided directory is, by
// construction, one-sided too, so this only ever triggers at the
// directory-presence level, not per file.
func (m Model) renderPanes(height int) (left, gutter, right []string) {
	if !m.cursorDir.Listed {
		msg := []string{dimStyle.Render("Loading…")}
		return msg, []string{""}, msg
	}

	children := m.cursorDir.Children
	if len(children) == 0 {
		msg := []string{dimStyle.Render("(empty)")}
		left, right = msg, msg
	} else {
		end := m.scrollOffset + height
		if end > len(children) {
			end = len(children)
		}
		for i := m.scrollOffset; i < end; i++ {
			l, g, r := renderRowTriple(children[i], m.sess, m.spinnerFrame, i == m.cursorIdx)
			left = append(left, l)
			gutter = append(gutter, g)
			right = append(right, r)
		}
	}

	// A directory missing on one side always renders as a single static
	// placeholder there, with no rows — even if it's empty on the side
	// that does exist, in which case this replaces the "(empty)" set
	// above. Checked last so it always wins.
	switch m.cursorDir.Presence {
	case diffmodel.LeftOnly:
		right = []string{placeholderStyle.Render(doesNotExistText)}
		gutter = []string{""}
	case diffmodel.RightOnly:
		left = []string{placeholderStyle.Render(doesNotExistText)}
		gutter = []string{""}
	}
	return left, gutter, right
}

// renderRowTriple renders one entry as (left name, gutter glyph, right
// name). The status glyph appears once, centered in the gutter, and the
// entry name on each side present is colored to match (SPEC.md §6: a
// distinct glyph and a distinct color per status, glyph never dropped
// in favor of color alone).
func renderRowTriple(n *tree.Node, sess *session.Session, spinnerFrame int, selected bool) (left, gutter, right string) {
	glyph, style := statusGlyph(n, sess, spinnerFrame)
	gutterCell := style.Render(glyph)

	nameTag := n.Name + typeGlyph(n.Type)
	leftName, rightName := nameTag, nameTag
	switch n.Presence {
	case diffmodel.LeftOnly:
		rightName = ""
	case diffmodel.RightOnly:
		leftName = ""
	}

	left = placeholderIfEmpty(styleIfNotEmpty(leftName, style))
	right = placeholderIfEmpty(styleIfNotEmpty(rightName, style))
	if selected {
		left = cursorStyle.Render(left)
		gutterCell = cursorStyle.Render(gutterCell)
		right = cursorStyle.Render(right)
	}
	return left, gutterCell, right
}

func styleIfNotEmpty(s string, style lipgloss.Style) string {
	if s == "" {
		return s
	}
	return style.Render(s)
}

// doesNotExistText spells out a whole pane being absent (the current
// directory itself doesn't exist on this side); rowPlaceholder marks a
// single row's missing counterpart more subtly, since the gutter's
// →/← glyph right next to it already says which side is missing.
const doesNotExistText = "<does not exist>"
const rowPlaceholder = "–"

func placeholderIfEmpty(s string) string {
	if s == "" {
		return placeholderStyle.Render(rowPlaceholder)
	}
	return s
}

func typeGlyph(t diffmodel.EntryType) string {
	switch t {
	case diffmodel.Dir:
		return "/"
	case diffmodel.Symlink:
		return "@"
	default:
		return ""
	}
}

// spinnerFrames are the animated pending-work glyph's frames — a growing
// "." / ".." / "..." sequence — cycled by the spinnerTickMsg driven from
// model.go. Used for both directory listings and comparisons still in
// flight or queued, replacing what used to be a single static "…" for
// both (SPEC.md §6).
var spinnerGlyphFrames = [spinnerFrames]string{".", "..", "..."}

func spinnerGlyph(frame int) string {
	return spinnerGlyphFrames[frame%len(spinnerGlyphFrames)]
}

// statusGlyph picks the glyph+style for a row. Every status pairs a
// distinct glyph with a distinct color (SPEC.md §6) so it reads even
// without color. The one-sided arrow points toward the side the entry
// exists on, not the side it's missing from.
func statusGlyph(n *tree.Node, sess *session.Session, spinnerFrame int) (string, lipgloss.Style) {
	switch n.Presence {
	case diffmodel.LeftOnly:
		return "←", missingStyle
	case diffmodel.RightOnly:
		return "→", missingStyle
	}

	if n.IsDir() {
		if !n.Listed && sess.IsListPending(n.RelPath) {
			return spinnerGlyph(spinnerFrame), pendingStyle
		}
		if n.ListErrLeft != nil || n.ListErrRight != nil {
			return "!", errorStyle
		}
		switch n.Rollup {
		case diffmodel.Same:
			return "=", sameStyle
		case diffmodel.Differs, diffmodel.CompareError:
			return "≠", differsStyle
		default:
			return "?", dimStyle
		}
	}

	if sess.IsComparePending(n.RelPath) {
		return spinnerGlyph(spinnerFrame), pendingStyle
	}
	switch n.Result {
	case diffmodel.Same:
		return "=", sameStyle
	case diffmodel.Differs:
		return "≠", differsStyle
	case diffmodel.CompareError:
		return "!", errorStyle
	default:
		return "?", dimStyle
	}
}

func (m Model) renderDetails() string {
	children := m.cursorDir.Children
	if m.cursorIdx >= len(children) {
		return padDetailsLines([]string{dimStyle.Render("(no selection)")})
	}
	n := children[m.cursorIdx]

	lines := []string{fmt.Sprintf("%s%s  [%s]", n.Name, typeGlyph(n.Type), presenceLabel(n.Presence))}
	if n.HaveStat {
		lines = append(lines, fmt.Sprintf("left:  size=%-10d mtime=%s", n.LeftSize, n.LeftMtime.Local().Format("2006-01-02 15:04:05")))
		lines = append(lines, fmt.Sprintf("right: size=%-10d mtime=%s", n.RightSize, n.RightMtime.Local().Format("2006-01-02 15:04:05")))
	}
	// Directories roll up results from potentially many descendants
	// compared at different levels, so a single "compared at" level isn't
	// meaningful for them — this line is file/symlink-only.
	if n.Presence == diffmodel.Both && !n.IsDir() {
		lines = append(lines, "compared by: "+compareLevelLabel(n.Level))
	}
	if n.Err != nil {
		lines = append(lines, errorStyle.Render("error: "+oneLine(n.Err.Error())))
	}
	return padDetailsLines(lines)
}

// padDetailsLines pads or truncates lines to exactly detailsContentLines
// entries, so the details panel always renders at the same fixed height
// no matter which fields are populated for the selected row (see
// detailsContentLines' doc comment in model.go for why that matters).
func padDetailsLines(lines []string) string {
	if len(lines) > detailsContentLines {
		lines = lines[:detailsContentLines]
	}
	for len(lines) < detailsContentLines {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// oneLine collapses an error message onto a single line — an embedded
// newline would otherwise defeat padDetailsLines' fixed line count, since
// it counts slice entries, not rendered terminal rows.
func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " ")
}

// compareLevelLabel names a CompareLevel for display in the details panel
// and status bar — so the user can tell at a glance whether a result
// reflects a cheap metadata check or an actual bytewise read.
func compareLevelLabel(level diffmodel.CompareLevel) string {
	switch level {
	case diffmodel.SizeMtime:
		return "metadata"
	case diffmodel.Checksum:
		return "content"
	default:
		return "none"
	}
}

func presenceLabel(p diffmodel.Presence) string {
	switch p {
	case diffmodel.LeftOnly:
		return "left only"
	case diffmodel.RightOnly:
		return "right only"
	default:
		return "both sides"
	}
}

func (m Model) renderStatusBar() string {
	st := m.sess.Stats()
	stats := fmt.Sprintf("Listing: %d pending, %d active · Comparing: %d pending, %d active",
		st.ListPending, st.ListActive, st.CmpPending, st.CmpActive)

	recursiveLabel := "off"
	if m.recursive {
		recursiveLabel = "on"
	}
	settings := fmt.Sprintf("[level: %s | recursive: %s] ", compareLevelLabel(m.compareLevel), recursiveLabel)
	hint := "↑/↓ move · →/Enter open · ←/Backspace up · l level · r recursive · c compare · n/N diff · x cancel · ? help · q quit"

	return statusBarStyle.Render(stats) + "\n" + pendingStyle.Render(settings) + dimStyle.Render(hint)
}

func helpView() string {
	lines := []string{
		titleStyle.Render("dirdiff — keybindings"),
		"",
		"↑ / ↓          move cursor",
		"PgUp/PgDn      move by page",
		"Home / End     jump to first / last entry",
		"→ / Enter      open directory (both panes navigate together)",
		"← / Backspace  up to parent directory",
		"l              switch compare level — metadata (size + date) ↔ content (byte-for-byte) (remembered)",
		"r              toggle recursive on/off (remembered, default on)",
		"c              compare current directory's entries at the current level/recursive setting",
		"n / N          jump to next / previous difference",
		"x              cancel all pending (not yet started) comparisons",
		"?              toggle this help",
		"q / Ctrl+C     quit",
		"",
		"Status glyphs:",
		sameStyle.Render("  =") + " same        " + differsStyle.Render("≠") + " differs        " + errorStyle.Render("!") + " error/unreadable",
		missingStyle.Render("  ←") + " only on left" + "  " + missingStyle.Render("→") + " only on right  " + dimStyle.Render("?") + " not yet compared",
		pendingStyle.Render("  ...") + " pending / in progress (animated)",
		"",
		dimStyle.Render("press ? or esc to close"),
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(lines, "\n"))
}

func displayPath(root, rel string) string {
	if rel == "" {
		return root
	}
	return root + "/" + rel
}

func truncate(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:width])
	}
	return string(r[:width-1]) + "…"
}
