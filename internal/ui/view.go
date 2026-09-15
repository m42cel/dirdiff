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

	paneWidth := m.width/2 - 2
	if paneWidth < 8 {
		paneWidth = 8
	}

	header := lipgloss.JoinHorizontal(lipgloss.Top,
		headerStyle.Width(paneWidth).Render(truncate(displayPath(m.sess.LeftRoot, m.cursorDir.RelPath), paneWidth)),
		headerStyle.Width(paneWidth).Render(truncate(displayPath(m.sess.RightRoot, m.cursorDir.RelPath), paneWidth)),
	)

	listHeight := m.listAreaHeight()
	leftLines, rightLines := m.renderPanes(listHeight)
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		paneStyle.Width(paneWidth).Height(listHeight).Render(strings.Join(leftLines, "\n")),
		paneStyle.Width(paneWidth).Height(listHeight).Render(strings.Join(rightLines, "\n")),
	)

	details := detailsStyle.Width(m.width - 2).Height(detailsPanelHeight - 1).Render(m.renderDetails())
	status := m.renderStatusBar()

	return header + "\n" + body + "\n" + details + "\n" + status
}

// renderPanes returns the visible lines for both panes. A directory
// missing on one side renders as a single static placeholder on that
// side, with no rows (SPEC.md §4.3) — everything under a one-sided
// directory is, by construction, one-sided too, so this only ever
// triggers at the directory-presence level, not per file.
func (m Model) renderPanes(height int) (left, right []string) {
	if !m.cursorDir.Listed {
		msg := []string{dimStyle.Render("Loading…")}
		return msg, msg
	}
	if len(m.cursorDir.Children) == 0 {
		msg := []string{dimStyle.Render("(empty)")}
		return msg, msg
	}

	children := m.cursorDir.Children
	end := m.scrollOffset + height
	if end > len(children) {
		end = len(children)
	}
	for i := m.scrollOffset; i < end; i++ {
		l, r := renderRowPair(children[i], m.sess, i == m.cursorIdx)
		left = append(left, l)
		right = append(right, r)
	}

	switch m.cursorDir.Presence {
	case diffmodel.LeftOnly:
		right = []string{placeholderStyle.Render("— does not exist —")}
	case diffmodel.RightOnly:
		left = []string{placeholderStyle.Render("— does not exist —")}
	}
	return left, right
}

func renderRowPair(n *tree.Node, sess *session.Session, selected bool) (left, right string) {
	glyph, style := statusGlyph(n, sess)
	prefix := style.Render(glyph) + " "

	nameTag := n.Name + typeGlyph(n.Type)
	leftName, rightName := nameTag, nameTag
	switch n.Presence {
	case diffmodel.LeftOnly:
		rightName = ""
	case diffmodel.RightOnly:
		leftName = ""
	}

	left = prefix + placeholderIfEmpty(leftName)
	right = prefix + placeholderIfEmpty(rightName)
	if selected {
		left = cursorStyle.Render(left)
		right = cursorStyle.Render(right)
	}
	return left, right
}

func placeholderIfEmpty(s string) string {
	if s == "" {
		return placeholderStyle.Render("··")
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

// statusGlyph picks the glyph+style for a row. Every status pairs a
// distinct glyph with a distinct color (SPEC.md §6) so it reads even
// without color.
func statusGlyph(n *tree.Node, sess *session.Session) (string, lipgloss.Style) {
	switch n.Presence {
	case diffmodel.LeftOnly:
		return "→", missingStyle
	case diffmodel.RightOnly:
		return "←", missingStyle
	}

	if n.IsDir() {
		if !n.Listed && sess.IsListPending(n.RelPath) {
			return "…", pendingStyle
		}
		if n.ListErrLeft != nil || n.ListErrRight != nil {
			return "!", errorStyle
		}
		switch n.Rollup {
		case diffmodel.Same:
			return "=", rollupSameStyle
		case diffmodel.Differs, diffmodel.CompareError:
			return "≠", differsStyle
		default:
			return "·", dimStyle
		}
	}

	if sess.IsComparePending(n.RelPath) {
		return "…", pendingStyle
	}
	switch n.Result {
	case diffmodel.Same:
		return "=", sameStyle
	case diffmodel.Differs:
		return "≠", differsStyle
	case diffmodel.CompareError:
		return "!", errorStyle
	default:
		return "·", dimStyle
	}
}

func (m Model) renderDetails() string {
	children := m.cursorDir.Children
	if m.cursorIdx >= len(children) {
		return dimStyle.Render("(no selection)")
	}
	n := children[m.cursorIdx]

	lines := []string{fmt.Sprintf("%s%s  [%s]", n.Name, typeGlyph(n.Type), presenceLabel(n.Presence))}
	if n.HaveStat {
		lines = append(lines, fmt.Sprintf("left:  size=%-10d mtime=%s", n.LeftSize, n.LeftMtime.Local().Format("2006-01-02 15:04:05")))
		lines = append(lines, fmt.Sprintf("right: size=%-10d mtime=%s", n.RightSize, n.RightMtime.Local().Format("2006-01-02 15:04:05")))
	} else if n.Presence == diffmodel.Both && !n.IsDir() {
		lines = append(lines, dimStyle.Render("(not yet compared — press 2/3/4)"))
	}
	if n.Err != nil {
		lines = append(lines, errorStyle.Render("error: "+n.Err.Error()))
	}
	return strings.Join(lines, "\n")
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

	hint := "↑/↓ move · →/Enter open · ←/Backspace up · 2/3/4 compare · r recursive · n/N diff · x cancel · ? help · q quit"
	if m.recursiveArmed {
		hint = pendingStyle.Render("[recursive armed] ") + hint
	}

	return statusBarStyle.Render(stats) + "\n" + dimStyle.Render(hint)
}

func helpView() string {
	lines := []string{
		headerStyle.Render("dirdiff — keybindings"),
		"",
		"↑ / ↓          move cursor",
		"PgUp/PgDn      move by page",
		"Home / End     jump to first / last entry",
		"→ / Enter      open directory (both panes navigate together)",
		"← / Backspace  up to parent directory",
		"2              compare current directory: size",
		"3              compare current directory: size + mtime",
		"4              compare current directory: checksum",
		"r              arm recursive — the next 2/3/4 applies to the whole subtree",
		"n / N          jump to next / previous difference",
		"x              cancel all pending (not yet started) comparisons",
		"?              toggle this help",
		"q / Ctrl+C     quit",
		"",
		"Status glyphs:",
		sameStyle.Render("  =") + " same        " + differsStyle.Render("≠") + " differs        " + errorStyle.Render("!") + " error/unreadable",
		missingStyle.Render("  →") + " missing right" + "  " + missingStyle.Render("←") + " missing left  " + dimStyle.Render("·") + " not yet compared",
		pendingStyle.Render("  …") + " pending / in progress",
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
