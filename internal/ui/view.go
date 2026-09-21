package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/pairtree"
	"github.com/m42cel/dirdiff/internal/sidetree"
)

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	if m.showHelp {
		return helpView()
	}
	if m.showFilterMenu {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, filterMenuView(m.filterCursor, m.filterEditing))
	}
	if m.showWorkersMenu {
		content := workersMenuView(m)
		if m.editingWorkers {
			content = workersInputView(m)
		}
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
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
	leftTitle := titleStyle.Render(truncate(m.paneTitle(diffmodel.Left), leftWidth))
	rightTitle := titleStyle.Render(truncate(m.paneTitle(diffmodel.Right), rightWidth))
	titleRow := lipgloss.JoinHorizontal(lipgloss.Top,
		titleCellStyle.Width(leftWidth+paneOverhead).Render(leftTitle),
		lipgloss.NewStyle().Width(gutterWidth).Render(""),
		titleCellStyle.Width(rightWidth+paneOverhead).Render(rightTitle),
	)

	// paneStyle's own horizontal padding (1 column each side) comes out of
	// leftWidth/rightWidth before any row text is rendered inside the box,
	// so row names must be truncated to this narrower content width, not
	// the box width, or a long name overflows into a wrapped second line.
	const panePadding = 2
	leftRows, gutterRows, rightRows := m.renderPanes(listHeight, leftWidth-panePadding, rightWidth-panePadding)
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
func (m Model) renderPanes(height, leftWidth, rightWidth int) (left, gutter, right []string) {
	if m.atRootParent {
		l, g, r := m.renderRootParentRow(leftWidth, rightWidth)
		return []string{l}, []string{g}, []string{r}
	}
	if !m.cursorDir.Listed() {
		msg := []string{dimStyle.Render("Loading…")}
		return msg, []string{""}, msg
	}

	children := m.visibleChildren()
	if len(children) == 0 {
		text := "(empty)"
		if len(m.cursorDir.Children) > 0 {
			text = "(no entries match filter)"
		}
		msg := []string{dimStyle.Render(text)}
		left, right = msg, msg
	} else {
		end := m.scrollOffset + height
		if end > len(children) {
			end = len(children)
		}
		for i := m.scrollOffset; i < end; i++ {
			c := children[i]
			dim := !m.filter.isAll() && !matchesFilter(c, m.filter)
			l, g, r := renderRowTriple(c, m.spinnerFrame, i == m.cursorIdx, dim, leftWidth, rightWidth)
			left = append(left, l)
			gutter = append(gutter, g)
			right = append(right, r)
		}
	}

	// A directory missing on one side always renders as a single static
	// placeholder there, with no rows — even if it's empty on the side
	// that does exist, in which case this replaces the "(empty)" set
	// above. Checked last so it always wins.
	switch m.cursorDir.Presence() {
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
// in favor of color alone). dim marks a row shown only because it has a
// descendant matching the active filter, not because it matches itself
// (SPEC.md §4.7) — faded to distinguish a path-through from a real hit.
func renderRowTriple(n *pairtree.Node, spinnerFrame int, selected, dim bool, leftWidth, rightWidth int) (left, gutter, right string) {
	glyph, style := statusGlyph(n, spinnerFrame)
	if dim {
		style = style.Faint(true)
	}
	gutterCell := style.Render(glyph)

	nameTag := n.Name() + typeGlyph(n.Type)
	leftName, rightName := nameTag, nameTag
	switch n.Presence() {
	case diffmodel.LeftOnly:
		rightName = ""
	case diffmodel.RightOnly:
		leftName = ""
	}

	// The listing-pending spinner appended below is its own unhighlighted
	// suffix, so a name long enough to need truncating must leave it room
	// up front — sized to the suffix's widest frame (" ..."), not
	// whichever frame happens to be showing, so the row never reflows as
	// the animation ticks.
	pendingLeft, pendingRight := n.PendingListing()
	leftBudget, rightBudget := leftWidth, rightWidth
	if n.IsDir() {
		if pendingLeft > 0 {
			leftBudget -= pendingSuffixWidth
		}
		if pendingRight > 0 {
			rightBudget -= pendingSuffixWidth
		}
	}
	leftName = truncate(leftName, leftBudget)
	rightName = truncate(rightName, rightBudget)

	left = placeholderIfEmpty(styleIfNotEmpty(leftName, style))
	right = placeholderIfEmpty(styleIfNotEmpty(rightName, style))
	if selected {
		left = cursorStyle.Render(left)
		gutterCell = cursorStyle.Render(gutterCell)
		right = cursorStyle.Render(right)
	}

	// A directory's own listing-pending indicator is per side — each
	// side's tree is listed independently, so a one-sided descendant's
	// still-running listing job only ever counts against the side it
	// exists on (SPEC.md §3.3) — and is appended after any cursor
	// highlighting above, in its own unhighlighted pendingStyle, so a
	// selected row's highlighted width stays constant instead of
	// growing and shrinking as the animation frame's length changes.
	if n.IsDir() {
		left += listingSuffix(pendingLeft, spinnerFrame)
		right += listingSuffix(pendingRight, spinnerFrame)
	}
	return left, gutterCell, right
}

// pendingSuffixWidth is the width listingSuffix's widest frame (" ...")
// takes, reserved up front so a truncated name doesn't reflow as the
// animation ticks through frames of different lengths.
const pendingSuffixWidth = 4

// listingSuffix is the animated indicator for a directory with listing
// work still outstanding on one side's subtree — empty when there is none.
func listingSuffix(pending, spinnerFrame int) string {
	if pending <= 0 {
		return ""
	}
	return pendingStyle.Render(" " + animGlyph(spinnerGlyphFrames, spinnerFrame))
}

// renderRootParentRow renders the single row shown above the pairing's
// two directories (SPEC.md §4.3.1): the pair itself, each side named the
// way a listing of its own parent would name it. Under a sub-compare the
// two names simply differ, which is precisely what that level was built
// for. It's always the cursor row, always present on both sides, and
// never dimmed by the filter, so none of renderRowTriple's per-row cases
// apply — but the gutter glyph and the per-side listing indicator are the
// pairing root's own, so a still-scanning tree reads the same here as
// anywhere else.
func (m Model) renderRootParentRow(leftWidth, rightWidth int) (left, gutter, right string) {
	root := m.cursorDir
	glyph, style := statusGlyph(root, m.spinnerFrame)

	pendingLeft, pendingRight := root.PendingListing()
	leftBudget, rightBudget := leftWidth, rightWidth
	if pendingLeft > 0 {
		leftBudget -= pendingSuffixWidth
	}
	if pendingRight > 0 {
		rightBudget -= pendingSuffixWidth
	}

	left = cursorStyle.Render(style.Render(truncate(m.rootRowLabel(diffmodel.Left)+"/", leftBudget)))
	right = cursorStyle.Render(style.Render(truncate(m.rootRowLabel(diffmodel.Right)+"/", rightBudget)))
	left += listingSuffix(pendingLeft, m.spinnerFrame)
	right += listingSuffix(pendingRight, m.spinnerFrame)
	return left, cursorStyle.Render(style.Render(glyph)), right
}

// rootRowLabel names one side of the pairing as a row of its own parent.
// A sub-compare's directory has a name; the compared roots don't — they
// *are* the tree — so they fall back to the last element of their path.
func (m Model) rootRowLabel(sd diffmodel.Side) string {
	if sn := m.root.Side(sd); sn != nil && sn.Name != "" {
		return sn.Name
	}
	return rootRowName(m.sideRoot(sd))
}

// rootRowName is how a root directory is named as a row of its own parent:
// its last path element. A root with no last element to call its own ("/",
// or a relative "." / "..") keeps its whole path instead, since a row
// labeled "." sitting under a pane titled "." says nothing.
func rootRowName(root string) string {
	switch base := filepath.Base(root); base {
	case ".", "..", string(filepath.Separator):
		return root
	default:
		return base
	}
}

func (m Model) sideRoot(sd diffmodel.Side) string {
	if sd == diffmodel.Right {
		return m.sess.RightRoot
	}
	return m.sess.LeftRoot
}

// sidePath is where a row actually lives on one side. Under a
// sub-compare the two sides are at unrelated paths, so this is the only
// honest thing to put above a pane — a pairing-relative path names a row
// in both panes at once but is a real path in neither.
//
// A row missing on that side has no path of its own; it's spelled as the
// nearest ancestor that does exist plus the rest of the way down, which
// is the path the entry would have if it were there.
func (m Model) sidePath(n *pairtree.Node, sd diffmodel.Side) string {
	var below []string
	for cur := n; cur != nil; cur = cur.Parent {
		if sn := cur.Side(sd); sn != nil {
			path := displayPath(m.sideRoot(sd), sn.RelPath)
			for i := len(below) - 1; i >= 0; i-- {
				path += "/" + below[i]
			}
			return path
		}
		below = append(below, cur.Name())
	}
	return m.sideRoot(sd)
}

// paneTitle is the path shown above a pane. Above the pairing (SPEC.md
// §4.3.1) that's its own parent directory — the level actually being
// stood in, even though none of its other entries are listed there.
func (m Model) paneTitle(sd diffmodel.Side) string {
	if m.atRootParent {
		return filepath.Dir(m.sidePath(m.root, sd))
	}
	return m.sidePath(m.cursorDir, sd)
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

// spinnerGlyphFrames is a growing "." / ".." / "..." sequence (SPEC.md
// §6), used for a directory's own per-side listing-pending indicator.
var spinnerGlyphFrames = []string{".", "..", "..."}

// comparePendingFrames animates any row whose comparison is still in
// flight or queued (SPEC.md §3.3/§6) — a file's own compare, or a
// directory whose subtree still has one outstanding. Both share the same
// braille sequence deliberately: there's no user-visible distinction
// between "this file is comparing" and "something inside this directory
// is comparing".
var comparePendingFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// animGlyph indexes any of the frame sequences above by the shared
// spinnerFrame tick, modulo that sequence's own length — sequences of
// different lengths cycle independently off the one clock (see
// spinnerInterval's doc comment in model.go).
func animGlyph(frames []string, frame int) string {
	return frames[frame%len(frames)]
}

// statusGlyph picks the glyph+style for a row. Every status pairs a
// distinct glyph with a distinct color (SPEC.md §6) so it reads even
// without color. The one-sided arrow points toward the side the entry
// exists on, not the side it's missing from.
func statusGlyph(n *pairtree.Node, spinnerFrame int) (string, lipgloss.Style) {
	// A one-sided row's arrow is already its final answer — there's
	// nothing to compare it against — so a metadata read still pending on
	// the side it does exist on doesn't replace it with a spinner; that
	// read only fills in a size for the details panel.
	switch n.Presence() {
	case diffmodel.LeftOnly:
		return "←", missingStyle
	case diffmodel.RightOnly:
		return "→", missingStyle
	}

	if n.IsDir() {
		if leftErr, rightErr := n.ListErrs(); leftErr != nil || rightErr != nil {
			return "!", errorStyle
		}
		// Differs/CompareError is checked before PendingCompare: it's
		// already the worst possible rollup (SPEC.md §3.3's monotonicity),
		// so further comparisons still in flight elsewhere in the subtree
		// can't change it back — showing the spinner here would just
		// flicker the glyph between "≠" and pending for no reason.
		if n.Result == diffmodel.Differs || n.Result == diffmodel.CompareError {
			return "≠", differsStyle
		}
		// Otherwise (Same or not-yet-known), an examination still
		// outstanding anywhere in the subtree means the rollup isn't
		// final yet — show that instead of the rollup glyph, since the
		// rollup only reflects completed results and would otherwise
		// misreport a subtree as "clean so far" or "not yet known" while
		// work is still in flight beneath it. Listing-pending is shown
		// separately, per side, next to the name (renderRowTriple):
		// listing is ambient discovery rather than something the user
		// asked for, so it doesn't belong in this shared gutter glyph
		// even though it, too, runs per side.
		if n.ExaminePending() {
			return animGlyph(comparePendingFrames, spinnerFrame), pendingStyle
		}
		switch n.Result {
		case diffmodel.Same:
			return "=", sameStyle
		default:
			return "?", dimStyle
		}
	}

	if n.ExaminePending() {
		return animGlyph(comparePendingFrames, spinnerFrame), pendingStyle
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
	children := m.visibleChildren()
	if m.cursorIdx >= len(children) {
		return padDetailsLines([]string{dimStyle.Render("(no selection)")})
	}
	n := children[m.cursorIdx]

	lines := []string{m.detailsTitle(n)}
	switch {
	case n.IsDir():
		// A directory's own size is meaningless here; what it contains is
		// the interesting number (SPEC.md §4.2), and it's a live one — both
		// counts and sizes grow as background listing and comparison
		// results arrive. A left-only/right-only directory has nothing at
		// all on its missing side — not a subtree that happens to total
		// zero — so that line names it as absent (the same wording as the
		// one-sided pane placeholder, §4.3) rather than being printed as
		// "0 directories · 0 files · 0 B", which would misreport absence
		// as an empty-but-existing directory.
		leftTotals, rightTotals := n.SideTotals()
		left, right := doesNotExistText, doesNotExistText
		if n.Presence() != diffmodel.RightOnly {
			left = totalsLabel(leftTotals)
		}
		if n.Presence() != diffmodel.LeftOnly {
			right = totalsLabel(rightTotals)
		}
		lines = append(lines, "left:  "+left, "right: "+right)
	case haveStat(n.Left) && haveStat(n.Right):
		lines = append(lines,
			fmt.Sprintf("left:  size=%-22s mtime=%s", fileSizeLabel(n.Left.Size), n.Left.Mtime.Local().Format("2006-01-02 15:04:05")),
			fmt.Sprintf("right: size=%-22s mtime=%s", fileSizeLabel(n.Right.Size), n.Right.Mtime.Local().Format("2006-01-02 15:04:05")))
	}
	if n.Presence() == diffmodel.Both {
		lines = append(lines, "compared by: "+nodeCompareLevelLabel(n))
	}
	if n.Err != nil {
		lines = append(lines, errorStyle.Render("error: "+oneLine(n.Err.Error())))
	}
	return padDetailsLines(lines)
}

// detailsTitle names the selected row. A pairing's root row has no name
// of its own — it *is* the two directories — so above it (SPEC.md
// §4.3.1) it's titled with both their paths instead of an empty name.
func (m Model) detailsTitle(n *pairtree.Node) string {
	if n == m.root {
		label := "compared roots"
		if len(m.stack) > 0 {
			label = "sub-compare"
		}
		// Two full paths can easily outrun the panel, which has to stay
		// exactly detailsContentLines tall (see padDetailsLines).
		return truncate(fmt.Sprintf("%s ↔ %s  [%s]",
			m.sidePath(n, diffmodel.Left), m.sidePath(n, diffmodel.Right), label), m.width)
	}
	return fmt.Sprintf("%s%s  [%s]", n.Name(), typeGlyph(n.Type), presenceLabel(n.Presence()))
}

// rowLabel names a row for a status-bar note. The pairing's root row has
// no name of its own, so it's named by the side being talked about.
func (m Model) rowLabel(n *pairtree.Node, sd diffmodel.Side) string {
	if n == m.root {
		return m.rootRowLabel(sd)
	}
	return n.Name()
}

func sideLabel(sd diffmodel.Side) string {
	if sd == diffmodel.Right {
		return "right"
	}
	return "left"
}

// haveStat reports whether one side of a row has had its metadata read.
// A row shows its two sizes and mtimes only when both sides have, since
// the point of the pair of lines is the comparison between them.
func haveStat(n *sidetree.Node) bool { return n != nil && n.HaveStat }

// totalsLabel summarizes one side of a directory's subtree: how many
// entries it holds, and how large they are.
//
// Size is only ever what a comparison already had to read (SPEC.md §4.2),
// so it's reported as a "≥" lower bound while any file under the directory
// hasn't been compared — with the sized/total count saying how much is
// still missing — and as an unknown "?" when none has been, which is the
// steady state under --level=none. A directory holding no files at all
// reports a plain 0 B: nothing is unknown there.
func totalsLabel(t sidetree.Totals) string {
	parts := []string{
		// "directory"/"directories" is spelled out in full rather than
		// abbreviated to "dirs" — it's the one count in the details panel
		// without an obvious shorter form.
		countLabel(t.Dirs, "directory", "directories"),
		countLabel(t.Files, "file", "files"),
	}
	if t.Symlinks > 0 {
		// Symlinks are counted apart from files because they're never
		// sized: comparing one reads its target string, not a file
		// (SPEC.md §7), so folding them into the file count would leave the
		// sized-file tally permanently short of it.
		parts = append(parts, countLabel(t.Symlinks, "link", "links"))
	}
	switch {
	case t.SizedFiles == 0 && t.Files > 0:
		parts = append(parts, "size ?")
	case t.SizedFiles < t.Files:
		parts = append(parts, fmt.Sprintf("≥%s (%d/%d files sized)", humanSize(t.Size), t.SizedFiles, t.Files))
	default:
		parts = append(parts, humanSize(t.Size))
	}
	return strings.Join(parts, " · ")
}

// countLabel pairs a count with its noun, taking the singular form for
// exactly one.
func countLabel(n int, singular, plural string) string {
	word := plural
	if n == 1 {
		word = singular
	}
	return fmt.Sprintf("%d %s", n, word)
}

// fileSizeLabel shows a single file's size human-readably but keeps the
// exact byte count alongside it: the metadata level calls two files
// different on an exact size mismatch, which rounded units can easily
// render as the same number.
func fileSizeLabel(bytes int64) string {
	if bytes < sizeUnit {
		return humanSize(bytes)
	}
	return fmt.Sprintf("%s (%d B)", humanSize(bytes), bytes)
}

// sizeUnit is 1024: sizes are shown in base-2 units (KiB, MiB, …), the
// ones that match how filesystems actually allocate.
const sizeUnit = 1024

var sizeUnits = []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}

// humanSize formats a byte count in base-2 units. One decimal place below
// 10 keeps small values informative ("1.4 MiB") without implying precision
// the rounding doesn't have.
func humanSize(bytes int64) string {
	if bytes < sizeUnit {
		return fmt.Sprintf("%d B", bytes)
	}
	// divisions counts how many times bytes was divided down, which is
	// 1-based into sizeUnits: one division lands on KiB, sizeUnits[0].
	v, divisions := float64(bytes), 0
	for v >= sizeUnit && divisions < len(sizeUnits) {
		v /= sizeUnit
		divisions++
	}
	if v < 10 {
		return fmt.Sprintf("%.1f %s", v, sizeUnits[divisions-1])
	}
	return fmt.Sprintf("%.0f %s", v, sizeUnits[divisions-1])
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

// nodeCompareLevelLabel is compareLevelLabel for a pairtree.Node, covering a
// directory whose descendants were compared at more than one level
// (pairtree.rollupLevel's LevelMixed) as "mixed" instead of picking one of
// them arbitrarily.
func nodeCompareLevelLabel(n *pairtree.Node) string {
	if n.IsDir() && n.LevelMixed {
		return "mixed"
	}
	return compareLevelLabel(n.Level)
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
	if where := m.whereLabel(); where != "" {
		stats += " · " + where
	}

	recursiveLabel := "off"
	if m.recursive {
		recursiveLabel = "on"
	}
	settings := fmt.Sprintf("[level: %s | recursive: %s | filter: %s | scan workers: %d | compare workers: %d]",
		compareLevelLabel(m.compareLevel), recursiveLabel, filterSetLabel(m.filter), m.sess.ListWorkers(), m.sess.CompareWorkers())

	// The note takes the hint line rather than a line of its own: the
	// panel's height has to stay fixed, and whatever just went wrong is
	// more use than the keybindings for a moment.
	last := dimStyle.Render("↑/↓ move · →/Enter open · ←/Backspace up · l level · r recursive · f filter · w workers · c compare row · C compare dir · [ ] mark · p pair · n/N diff · x cancel · ? help · q quit")
	if m.note != "" {
		last = pendingStyle.Render(m.note)
	}

	return statusBarStyle.Render(truncate(stats, m.width)) + "\n" +
		pendingStyle.Render(settings) + "\n" + truncate(last, m.width)
}

// whereLabel says which pairing is on screen and what marks are waiting,
// when either is worth saying: the root pairing with nothing marked is
// the ordinary case and says nothing at all.
func (m Model) whereLabel() string {
	var parts []string
	if len(m.stack) > 0 {
		parts = append(parts, fmt.Sprintf("sub-compare: %s ↔ %s",
			m.rootRowLabel(diffmodel.Left), m.rootRowLabel(diffmodel.Right)))
	}
	if marks := m.marksLabel(); marks != "" {
		parts = append(parts, marks)
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, " | ") + "]"
}

func (m Model) marksLabel() string {
	if m.markLeft == nil && m.markRight == nil {
		return ""
	}
	return fmt.Sprintf("marked: %s ↔ %s (p to pair)", markLabel(m.markLeft), markLabel(m.markRight))
}

// markLabel names a marked directory by its path below its own root,
// which is what distinguishes two marks that share a basename.
func markLabel(n *sidetree.Node) string {
	switch {
	case n == nil:
		return "–"
	case n.RelPath == "":
		return "/"
	default:
		return n.RelPath
	}
}

func helpView() string {
	lines := []string{
		titleStyle.Render("dirdiff — keybindings"),
		"",
		"↑ / ↓          move cursor",
		"PgUp/PgDn      move by page",
		"Home / End     jump to first / last entry",
		"→ / Enter      open directory (both panes navigate together)",
		"← / Backspace  up to parent directory — at the top, up to the two compared directories as a single row (whole-subtree totals); past that, out of a sub-compare",
		"l              switch compare level — metadata (size + date) ↔ content (byte-for-byte) (remembered)",
		"r              toggle recursive on/off (remembered, default on)",
		"f              open row-status filter popup: multi-select Left-only / Right-only / Equal / Different — space toggles, enter confirms (remembered)",
		"w              open worker-count popup: scan / compare pool size, Enter to type a new value",
		"c              compare the selected row at the current level/recursive setting — a file on its own, a directory's entries (or whole subtree, with r)",
		"C              compare the current directory the same way, whatever the cursor is on and whatever the filter hides",
		"[ / ]          mark the selected row's left / right side as one end of a sub-compare (marks survive navigating anywhere)",
		"p              pair the two marks: compare those directories against each other, whatever their paths — ← past the top row leaves again",
		"n / N          jump to next / previous difference",
		"x              cancel all pending (not yet started) comparisons",
		"?              toggle this help",
		"q / Ctrl+C     quit",
		"",
		"Status glyphs:",
		sameStyle.Render("  =") + " same        " + differsStyle.Render("≠") + " differs        " + errorStyle.Render("!") + " error/unreadable",
		missingStyle.Render("  ←") + " only on left" + "  " + missingStyle.Render("→") + " only on right  " + dimStyle.Render("?") + " not yet compared",
		pendingStyle.Render("  ⠋") + " comparing: file or directory subtree, gutter (animated)",
		pendingStyle.Render("  name...") + " directory: listing pending on that side, next to the name (animated)",
		"",
		dimStyle.Render("press ? or esc to close"),
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(lines, "\n"))
}

// filterMenuView renders the 'f' popup (SPEC.md §4.7): a small box listing
// every FilterStatus option as a checkbox reflecting editing's in-progress
// selection, with cursor (the currently highlighted row) marked by
// cursorStyle — highlight and selection are independent, so a highlighted
// row isn't necessarily checked and vice versa.
func filterMenuView(cursor int, editing FilterSet) string {
	lines := []string{titleStyle.Render("Filter rows"), ""}
	for i, f := range allFilters {
		box := "[ ]"
		if editing[f] {
			box = "[x]"
		}
		line := "  " + box + " " + filterLabel(f)
		if i == cursor {
			line = cursorStyle.Render("> " + box + " " + filterLabel(f))
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", dimStyle.Render("↑/↓ select · Space toggle · Enter confirm · Esc cancel"))
	return popupStyle.Render(strings.Join(lines, "\n"))
}

// workersMenuView renders the 'w' popup's row-select list (SPEC.md §4.8):
// the scan and compare worker pools with their live values. Editing opens
// a separate popup (workersInputView) rather than replacing a row's text
// in place.
func workersMenuView(m Model) string {
	rows := workersMenuRows(m)

	lines := []string{titleStyle.Render("Worker counts"), ""}
	for i, r := range rows {
		text := fmt.Sprintf("%s: %d", r.label, r.value)
		if i == m.workersCursor {
			lines = append(lines, cursorStyle.Render("> "+text))
		} else {
			lines = append(lines, "  "+text)
		}
	}

	lines = append(lines, "", dimStyle.Render("↑/↓ select · Enter edit · Esc close"))
	return popupStyle.Render(strings.Join(lines, "\n"))
}

// workersInputView renders the separate popup shown while editing one
// row's value (SPEC.md §4.8): its own dialog, on top of the row-select
// list, named after the row being edited and showing only the
// in-progress typed digits.
func workersInputView(m Model) string {
	label := workersMenuRows(m)[m.workersCursor].label
	lines := []string{
		titleStyle.Render(label),
		"",
		"  " + m.workersInput + "▏",
		"",
		dimStyle.Render("0-9 type · Enter apply · Esc cancel"),
	}
	return popupStyle.Render(strings.Join(lines, "\n"))
}

func workersMenuRows(m Model) []struct {
	label string
	value int
} {
	return []struct {
		label string
		value int
	}{
		{"Scan workers", m.sess.ListWorkers()},
		{"Compare workers", m.sess.CompareWorkers()},
	}
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
