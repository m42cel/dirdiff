// Package ui is the Bubble Tea presentation layer for dirdiff: it owns
// cursor/navigation state and key handling, and renders the tree that
// package session maintains in the background. It never does filesystem
// I/O itself — everything here either reads session/tree state or calls
// a session method to enqueue work.
package ui

import (
	"fmt"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/pairtree"
	"github.com/m42cel/dirdiff/internal/scan"
	"github.com/m42cel/dirdiff/internal/session"
	"github.com/m42cel/dirdiff/internal/sidetree"
)

const (
	// paneBoxOverhead is the top+bottom border lines of the full-box pane
	// frame; paneTitleRows is the path-title row rendered above that box
	// (see view.go) — both are vertical space spent before any list row
	// is drawn.
	paneBoxOverhead = 2
	paneTitleRows   = 1

	// detailsContentLines is the fixed number of interior lines the
	// details panel always renders — the name row, up to two stat rows, a
	// "compared by" row, and an error row. lipgloss's Height() is a
	// floor, not a ceiling, so renderDetails pads/truncates its output to
	// exactly this many lines to keep the panel's total height constant
	// regardless of which fields the selected row populates.
	detailsContentLines = 5
	detailsPanelHeight  = detailsContentLines + 1 // +1 for the top border
	statusBarHeight     = 3

	// spinnerInterval is how often pending-work glyphs advance to their
	// next animation frame (see animGlyph in view.go). Every such glyph
	// shares the one tick and counter below — each indexes it modulo its
	// own frame count, so frame sequences of different lengths (a file's
	// "." / ".." / "..." vs. a directory's Braille spinner) cycle
	// independently off the same clock without needing their own timer.
	spinnerInterval = 400 * time.Millisecond
)

// view is one pairing on screen and where the cursor stands in it. A
// sub-compare pushes a new one and pops back to what it was opened from,
// so this is exactly the state that is per-view rather than global.
type view struct {
	pairing   session.PairingID
	root      *pairtree.Node // the pairing's root row
	cursorDir *pairtree.Node

	cursorIdx    int
	scrollOffset int

	// atRootParent is the one level that isn't a real directory listing
	// (SPEC.md §4.3.1): standing above the pairing's two directories,
	// where the only row is the pair itself, so their whole-subtree
	// totals are readable in the details panel. cursorDir stays the
	// pairing root throughout — this level has no node of its own, since
	// the two directories' actual parents are unrelated to each other and
	// are never listed or compared. In a sub-compare it's also the way
	// out: ← from there pops back.
	atRootParent bool
}

// selection is a sub-compare being chosen, one side at a time: the panes
// navigate in lockstep (SPEC.md §4.1), so there is no moment at which the
// cursor stands in two unrelated directories — instead 'p' starts this,
// Space chooses the directory under the cursor for whichever side is
// being picked, and the second choice opens the pairing.
type selection struct {
	// side is the side being chosen right now; left is what was chosen
	// for the left side, once side has moved on to Right.
	side diffmodel.Side
	left *sidetree.Node

	// origin is where 'p' was pressed. Cancelling restores it, and it —
	// not wherever the hunt for the second directory ended — is what goes
	// on the view stack, so leaving the sub-compare later lands where the
	// whole operation started.
	origin view
}

// Model is the root Bubble Tea model.
type Model struct {
	sess *session.Session

	// view is the pairing being shown; stack holds the ones it was opened
	// from, outermost first. The root pairing is always at the bottom and
	// is never popped.
	view
	stack []view

	// picking is the sub-compare being set up (SPEC.md §4.9), or nil when
	// there is none. It's replaced wholesale rather than mutated, so a
	// copy of the Model never shares one with its successor.
	picking *selection

	// note is a one-off line for the status bar — why a keypress did
	// nothing, usually — shown until the next keypress.
	note string

	showHelp bool

	// compareLevel and recursive carry over between 'c' presses until
	// changed again with 'l' / 'r'. Defaults match the CLI's own default
	// (metadata, recursive) so the in-app picker starts in the same state
	// as the background auto-compare.
	compareLevel diffmodel.CompareLevel
	recursive    bool

	// filter is the persistent, multi-select row-status filter (SPEC.md
	// §4.7), changed via the 'f' popup the same way 'l'/'r' change their
	// own settings. showFilterMenu/filterCursor/filterEditing are the
	// popup's own open/highlight/in-progress-selection state, separate
	// from filter itself so cancelling with Esc leaves filter untouched —
	// filterEditing starts as a copy of filter when the popup opens and
	// is only copied back into filter on a confirming Enter.
	// They, and the filter below, are global rather than per view: a
	// sub-compare is a different pair of directories, not a different
	// set of preferences about how to compare.
	filter         FilterSet
	showFilterMenu bool
	filterCursor   int
	filterEditing  FilterSet

	// showWorkersMenu/workersCursor mirror showFilterMenu/filterCursor for
	// the 'w' popup (SPEC.md §4.8), which edits session's own live worker
	// counts directly rather than a Model-held setting — there's nothing
	// here to apply on confirm beyond what editingWorkers/workersInput
	// already did. workersCursor selects between the two rows: 0 = scan
	// (listing) workers, 1 = compare workers.
	showWorkersMenu bool
	workersCursor   int
	editingWorkers  bool
	workersInput    string

	spinnerFrame int

	width, height int
}

// New builds the initial model bound to sess, showing the root pairing.
func New(sess *session.Session) Model {
	root := sess.Tree()
	return Model{
		sess:         sess,
		view:         view{pairing: session.RootPairing, root: root, cursorDir: root},
		compareLevel: diffmodel.SizeMtime,
		recursive:    true,
		filter:       defaultFilterSet(),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		waitListResult(m.sess.ListResults()),
		waitStatResult(m.sess.StatResults()),
		waitCompareResult(m.sess.CompareResults()),
		tickSpinner())
}

type listResultMsg struct{ r scan.ListResult }
type statResultMsg struct{ r scan.StatResult }
type compareResultMsg struct{ r session.CompareResult }
type spinnerTickMsg struct{}

func waitListResult(ch <-chan scan.ListResult) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return nil
		}
		return listResultMsg{r}
	}
}

func waitStatResult(ch <-chan scan.StatResult) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return nil
		}
		return statResultMsg{r}
	}
}

func waitCompareResult(ch <-chan session.CompareResult) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return nil
		}
		return compareResultMsg{r}
	}
}

func tickSpinner() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureCursorVisible()
		return m, nil

	case listResultMsg:
		m.sess.OnListResult(msg.r)
		m.clampCursor()
		return m, waitListResult(m.sess.ListResults())

	case statResultMsg:
		m.sess.OnStatResult(msg.r)
		m.clampCursor()
		return m, waitStatResult(m.sess.StatResults())

	case compareResultMsg:
		m.sess.OnCompareResult(msg.r)
		m.clampCursor()
		return m, waitCompareResult(m.sess.CompareResults())

	case spinnerTickMsg:
		m.spinnerFrame++
		return m, tickSpinner()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey dispatches a key event. A terminal read can legitimately
// deliver several quickly-typed runes as a single tea.KeyMsg (e.g. an 'l'
// then 'c' sequence typed fast, SPEC.md §9) — each rune is processed in
// order as its own logical keypress so that still works exactly as if
// they'd arrived in separate messages.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		var cmd tea.Cmd
		for _, r := range msg.Runes {
			var tm tea.Model
			tm, cmd = m.handleSingleKey(string(r))
			m = tm.(Model)
			if cmd != nil {
				return m, cmd // e.g. quit — stop processing the rest
			}
		}
		return m, nil
	}
	return m.handleSingleKey(msg.String())
}

func (m Model) handleSingleKey(key string) (tea.Model, tea.Cmd) {
	if m.showHelp {
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "?", "esc":
			m.showHelp = false
		}
		return m, nil
	}

	if m.showFilterMenu {
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up":
			if m.filterCursor > 0 {
				m.filterCursor--
			}
		case "down":
			if m.filterCursor < len(allFilters)-1 {
				m.filterCursor++
			}
		case " ":
			f := allFilters[m.filterCursor]
			m.filterEditing[f] = !m.filterEditing[f]
		case "enter":
			// Committing an empty set would hide every row with no way
			// back in from the popup itself, so Enter is a no-op until
			// at least one status is selected — the popup just stays
			// open.
			if !m.filterEditing.isEmpty() {
				m.filter = m.filterEditing
				m.showFilterMenu = false
				m.clampCursor()
			}
		case "f", "esc":
			m.showFilterMenu = false
		}
		return m, nil
	}

	if m.showWorkersMenu {
		if m.editingWorkers {
			switch key {
			case "enter":
				m.applyWorkersInput()
				m.editingWorkers = false
				m.workersInput = ""
				m.showWorkersMenu = false
			case "backspace":
				if len(m.workersInput) > 0 {
					m.workersInput = m.workersInput[:len(m.workersInput)-1]
				}
			case "esc":
				m.editingWorkers = false
				m.workersInput = ""
			default:
				// Only digits are meaningful for a worker count; a max of
				// 6 is far past any sane pool size but keeps the field
				// from growing unbounded on a stuck/repeated key.
				if len(key) == 1 && key[0] >= '0' && key[0] <= '9' && len(m.workersInput) < 6 {
					m.workersInput += key
				}
			}
			return m, nil
		}
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up":
			if m.workersCursor > 0 {
				m.workersCursor--
			}
		case "down":
			if m.workersCursor < 1 {
				m.workersCursor++
			}
		case "enter":
			m.editingWorkers = true
			m.workersInput = ""
		case "w", "esc":
			m.showWorkersMenu = false
		}
		return m, nil
	}

	// A note explains why the *last* keypress did nothing, so it lives
	// exactly until the next one.
	m.note = ""

	if m.picking != nil {
		return m.handleSelectionKey(key)
	}

	switch key {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "?":
		m.showHelp = true
	case "l":
		m.cycleCompareLevel()
	case "r":
		m.recursive = !m.recursive
	case "f":
		m.showFilterMenu = true
		m.filterCursor = 0
		m.filterEditing = cloneFilterSet(m.filter)
	case "w":
		m.showWorkersMenu = true
		m.workersCursor = 0
		m.editingWorkers = false
		m.workersInput = ""
	case "c":
		m.triggerCompare()
	case "C":
		m.triggerCompareDir()
	case "p":
		m.startSelection()
	case "n":
		m.jumpDiff(true)
	case "N":
		m.jumpDiff(false)
	case "x":
		m.sess.CancelPendingCompares()
	default:
		m.navigate(key)
	}
	return m, nil
}

// handleSelectionKey dispatches a keypress while a sub-compare is being
// chosen (SPEC.md §4.9). The mode *adds* a key rather than rebinding
// any: every movement key — →/Enter to descend included — keeps meaning
// exactly what it means outside, so there is nothing to unlearn on the
// way in or out, and no way to confirm a choice by reflex.
func (m Model) handleSelectionKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "?":
		m.showHelp = true
	case " ":
		m.choose()
	case "esc", "p":
		m.cancelSelection()
	default:
		m.navigate(key)
	}
	return m, nil
}

// navigate handles the movement keys, which are shared by the normal
// view and sub-compare selection. It reports whether key was one of
// them.
func (m *Model) navigate(key string) bool {
	switch key {
	case "up":
		if m.cursorIdx > 0 {
			m.cursorIdx--
			m.ensureCursorVisible()
		}
	case "down":
		if m.cursorIdx < len(m.visibleChildren())-1 {
			m.cursorIdx++
			m.ensureCursorVisible()
		}
	case "pgup":
		m.moveCursor(-m.listAreaHeight())
	case "pgdown":
		m.moveCursor(m.listAreaHeight())
	case "home":
		m.cursorIdx = 0
		m.ensureCursorVisible()
	case "end":
		if n := len(m.visibleChildren()); n > 0 {
			m.cursorIdx = n - 1
		}
		m.ensureCursorVisible()
	case "right", "enter":
		m.enter()
	case "left", "backspace":
		m.ascend()
	default:
		return false
	}
	return true
}

func (m *Model) moveCursor(delta int) {
	n := len(m.visibleChildren())
	if n == 0 {
		return
	}
	m.cursorIdx += delta
	if m.cursorIdx < 0 {
		m.cursorIdx = 0
	}
	if m.cursorIdx > n-1 {
		m.cursorIdx = n - 1
	}
	m.ensureCursorVisible()
}

// clampCursor keeps cursorIdx within the currently visible (filtered)
// child list (SPEC.md §4.7) — called after anything that can shrink that
// list out from under the cursor: a new list/compare result changing what
// matches the active filter, or the filter itself changing.
func (m *Model) clampCursor() {
	n := len(m.visibleChildren())
	if m.cursorIdx >= n {
		m.cursorIdx = n - 1
	}
	if m.cursorIdx < 0 {
		m.cursorIdx = 0
	}
	m.ensureCursorVisible()
}

// cycleCompareLevel switches the persistent compareLevel setting to the
// other of the two triggered levels ('l', SPEC.md §5.1/§9). It only
// changes what 'c' will run next time — it does not itself enqueue any
// work.
func (m *Model) cycleCompareLevel() {
	if m.compareLevel == diffmodel.SizeMtime {
		m.compareLevel = diffmodel.Checksum
	} else {
		m.compareLevel = diffmodel.SizeMtime
	}
}

// applyWorkersInput parses the 'w' popup's typed digits and resizes
// whichever pool workersCursor has selected (SPEC.md §4.8). An empty
// input (Enter pressed without typing anything) is treated as "cancel
// this edit," not "set to 0" — Session.Set*Workers clamps to at least 1
// anyway, but silently discarding an accidental bare Enter is friendlier
// than resetting the pool to 1.
func (m *Model) applyWorkersInput() {
	if m.workersInput == "" {
		return
	}
	n, err := strconv.Atoi(m.workersInput)
	if err != nil {
		return
	}
	switch m.workersCursor {
	case 0:
		m.sess.SetListWorkers(n)
	case 1:
		m.sess.SetCompareWorkers(n)
	}
}

// triggerCompare runs the persistent compareLevel setting on the row
// under the cursor ('c', SPEC.md §5.2): a file compares just itself, a
// directory compares its children — or its whole subtree if the
// persistent recursive toggle is on. Above the root (SPEC.md §4.3.1) the
// selected row is the root pair itself, so 'c' there compares the roots,
// which is what that level is for.
//
// Neither setting is consumed by the trigger, so 'c' can be pressed
// repeatedly (e.g. while navigating) without re-selecting them each time.
func (m *Model) triggerCompare() {
	visible := m.visibleChildren()
	if m.cursorIdx >= len(visible) {
		return
	}
	m.sess.TriggerCompare(m.pairing, visible[m.cursorIdx], m.compareLevel, m.recursive)
}

// triggerCompareDir runs the same settings on the whole directory being
// stood in ('C'), whether or not the filter is hiding some of it: the
// filter is a view concern, and a recursive trigger reaches hidden
// descendants anyway.
func (m *Model) triggerCompareDir() {
	m.sess.TriggerCompare(m.pairing, m.cursorDir, m.compareLevel, m.recursive)
}

// startSelection begins choosing a sub-compare ('p', SPEC.md §4.9),
// starting with its left side.
func (m *Model) startSelection() {
	m.picking = &selection{side: diffmodel.Left, origin: m.view}
}

// cancelSelection abandons the selection and puts the view back where it
// started, since navigating around to find a directory was incidental to
// an operation that didn't happen.
func (m *Model) cancelSelection() {
	m.view = m.picking.origin
	m.picking = nil
	m.sess.Navigate(m.pairing, m.cursorDir)
}

// choose takes the directory under the cursor as the side currently
// being picked (Space). The first choice moves on to the other side; the
// second opens the pairing. It's a no-op, with a note, on a row that has
// no such side or isn't a directory — pairing two files would be a
// one-row view of no value — and the selection stays open so the next
// candidate is one keypress away.
func (m *Model) choose() {
	visible := m.visibleChildren()
	if m.cursorIdx >= len(visible) {
		return
	}
	n := visible[m.cursorIdx]
	sd := m.picking.side
	sn := n.Side(sd)
	switch {
	case sn == nil:
		m.note = fmt.Sprintf("%s doesn't exist on the %s — choose one that does", m.rowLabel(n, sd), sideLabel(sd))
		return
	case !sn.IsDir():
		m.note = "a sub-compare pairs two directories, not files"
		return
	}
	if sd == diffmodel.Left {
		m.picking = &selection{side: diffmodel.Right, left: sn, origin: m.picking.origin}
		return
	}
	m.openSubCompare(m.picking.left, sn)
}

// openSubCompare pairs the two chosen directories and pushes the result
// on the view stack (SPEC.md §4.9).
func (m *Model) openSubCompare(left, right *sidetree.Node) {
	id, err := m.sess.OpenPairing(left, right)
	if err != nil {
		m.note = err.Error()
		return
	}
	p, ok := m.sess.Pairing(id)
	if !ok {
		return
	}
	m.stack = append(m.stack, m.picking.origin)
	m.view = view{pairing: id, root: p.Root, cursorDir: p.Root}
	m.picking = nil
	m.sess.Navigate(id, p.Root)
}

// popSubCompare leaves the current sub-compare for whichever view it was
// opened from, dropping it (SPEC.md §4.9): a sub-compare lives only
// while you're in it. Re-entering the same pair rebuilds it from the
// side trees, which is instant for everything but the bytes.
func (m *Model) popSubCompare() {
	if len(m.stack) == 0 {
		return // the root pairing is the bottom; there's nowhere to go
	}
	m.sess.ClosePairing(m.pairing)
	m.view = m.stack[len(m.stack)-1]
	m.stack = m.stack[:len(m.stack)-1]
	m.sess.Navigate(m.pairing, m.cursorDir)
}

// enter navigates into the directory under the cursor. Only directories
// are navigable — files aren't "opened" (no content viewer, SPEC.md
// §10). A directory missing on one side is still navigable as long as it
// exists on the other (SPEC.md §4.3).
func (m *Model) enter() {
	if m.atRootParent {
		// The only row up there is the pairing itself, so entering it is
		// simply the way back down into the normal view.
		m.atRootParent = false
		m.cursorIdx = 0
		m.scrollOffset = 0
		m.sess.Navigate(m.pairing, m.cursorDir)
		return
	}
	visible := m.visibleChildren()
	if m.cursorIdx >= len(visible) {
		return
	}
	target := visible[m.cursorIdx]
	if target.Type != diffmodel.Dir {
		return
	}
	m.cursorDir = target
	m.cursorIdx = 0
	m.scrollOffset = 0
	m.sess.Navigate(m.pairing, target)
}

// ascend moves to the parent directory, restoring the cursor to the
// child row we came from — found within the parent's own visible
// (filtered) list, since that's what index the cursor addresses.
func (m *Model) ascend() {
	parent := m.cursorDir.Parent
	if parent == nil {
		// Above the pairing's root there's one more level to go up to —
		// the pair itself as a single row (SPEC.md §4.3.1). Past that,
		// a sub-compare leaves for the view it was opened from, and the
		// root pairing has nowhere left to go.
		if !m.atRootParent {
			m.atRootParent = true
			m.cursorIdx = 0
			m.scrollOffset = 0
			return
		}
		if m.picking != nil {
			// Leaving the pairing mid-choice would close the very view the
			// selection started in, so the way out is to finish or cancel.
			m.note = "finish the sub-compare selection or cancel it (Esc) before leaving"
			return
		}
		m.popSubCompare()
		return
	}
	child := m.cursorDir
	m.cursorDir = parent
	m.cursorIdx = 0
	for i, c := range filterChildren(parent.Children, m.filter) {
		if c == child {
			m.cursorIdx = i
			break
		}
	}
	m.scrollOffset = 0
	m.ensureCursorVisible()
	m.sess.Navigate(m.pairing, parent)
}

// jumpDiff moves the cursor to the next (or previous) visible row in the
// current directory whose status isn't "same" — SPEC.md §9's 'n'/'N'. It
// only considers rows already compared at some level; it wraps around.
func (m *Model) jumpDiff(forward bool) {
	visible := m.visibleChildren()
	n := len(visible)
	if n == 0 {
		return
	}
	step := 1
	if !forward {
		step = -1
	}
	i := m.cursorIdx
	for k := 0; k < n; k++ {
		i = ((i+step)%n + n) % n
		if isDiffering(visible[i]) {
			m.cursorIdx = i
			m.ensureCursorVisible()
			return
		}
	}
}

func isDiffering(n *pairtree.Node) bool {
	if n.Presence() != diffmodel.Both {
		return true
	}
	return n.Result == diffmodel.Differs || n.Result == diffmodel.CompareError
}

func (m *Model) ensureCursorVisible() {
	h := m.listAreaHeight()
	if m.cursorIdx < m.scrollOffset {
		m.scrollOffset = m.cursorIdx
	}
	if m.cursorIdx >= m.scrollOffset+h {
		m.scrollOffset = m.cursorIdx - h + 1
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

func (m Model) listAreaHeight() int {
	h := m.height - paneBoxOverhead - paneTitleRows - detailsPanelHeight - statusBarHeight
	if h < 1 {
		h = 1
	}
	return h
}
