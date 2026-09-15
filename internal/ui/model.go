// Package ui is the Bubble Tea presentation layer for dirdiff: it owns
// cursor/navigation state and key handling, and renders the tree that
// package session maintains in the background. It never does filesystem
// I/O itself — everything here either reads session/tree state or calls
// a session method to enqueue work.
package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/scan"
	"github.com/m42cel/dirdiff/internal/session"
	"github.com/m42cel/dirdiff/internal/tree"
)

const (
	headerHeight       = 1
	detailsPanelHeight = 4
	statusBarHeight    = 2
)

// Model is the root Bubble Tea model.
type Model struct {
	sess *session.Session

	cursorDir      *tree.Node
	cursorIdx      int
	scrollOffset   int
	recursiveArmed bool
	showHelp       bool

	width, height int
}

// New builds the initial model bound to sess.
func New(sess *session.Session) Model {
	return Model{sess: sess, cursorDir: sess.Tree}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(waitListResult(m.sess.ListResults()), waitCompareResult(m.sess.CompareResults()))
}

type listResultMsg struct{ r scan.ListResult }
type compareResultMsg struct{ r scan.CompareOutcome }

func waitListResult(ch <-chan scan.ListResult) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return nil
		}
		return listResultMsg{r}
	}
}

func waitCompareResult(ch <-chan scan.CompareOutcome) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return nil
		}
		return compareResultMsg{r}
	}
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

	case compareResultMsg:
		m.sess.OnCompareResult(msg.r)
		return m, waitCompareResult(m.sess.CompareResults())

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey dispatches a key event. A terminal read can legitimately
// deliver several quickly-typed runes as a single tea.KeyMsg (e.g. the
// 'r' then '2'/'3'/'4' recursive-arm sequence, SPEC.md §9, typed fast) —
// each rune is processed in order as its own logical keypress so that
// still works exactly as if they'd arrived in separate messages.
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

	switch key {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "?":
		m.showHelp = true
	case "up":
		if m.cursorIdx > 0 {
			m.cursorIdx--
			m.ensureCursorVisible()
		}
	case "down":
		if m.cursorIdx < len(m.cursorDir.Children)-1 {
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
		if n := len(m.cursorDir.Children); n > 0 {
			m.cursorIdx = n - 1
		}
		m.ensureCursorVisible()
	case "right", "enter":
		m.enter()
	case "left", "backspace":
		m.ascend()
	case "2":
		m.trigger(diffmodel.Size)
	case "3":
		m.trigger(diffmodel.SizeMtime)
	case "4":
		m.trigger(diffmodel.Checksum)
	case "r":
		m.recursiveArmed = !m.recursiveArmed
	case "n":
		m.jumpDiff(true)
	case "N":
		m.jumpDiff(false)
	case "x":
		m.sess.CancelPendingCompares()
	}
	return m, nil
}

func (m *Model) moveCursor(delta int) {
	n := len(m.cursorDir.Children)
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

func (m *Model) clampCursor() {
	n := len(m.cursorDir.Children)
	if m.cursorIdx >= n {
		m.cursorIdx = n - 1
	}
	if m.cursorIdx < 0 {
		m.cursorIdx = 0
	}
	m.ensureCursorVisible()
}

// trigger runs the given comparison level on the current directory's
// children (SPEC.md §5.2), recursively if 'r' was pressed first.
func (m *Model) trigger(level diffmodel.CompareLevel) {
	m.sess.TriggerCompare(m.cursorDir, level, m.recursiveArmed)
	m.recursiveArmed = false
}

// enter navigates into the directory under the cursor. Only directories
// are navigable — files aren't "opened" (no content viewer, SPEC.md
// §10). A directory missing on one side is still navigable as long as it
// exists on the other (SPEC.md §4.3).
func (m *Model) enter() {
	if m.cursorIdx >= len(m.cursorDir.Children) {
		return
	}
	target := m.cursorDir.Children[m.cursorIdx]
	if target.Type != diffmodel.Dir {
		return
	}
	m.cursorDir = target
	m.cursorIdx = 0
	m.scrollOffset = 0
	m.sess.Navigate(target)
}

// ascend moves to the parent directory, restoring the cursor to the
// child row we came from.
func (m *Model) ascend() {
	parent := m.cursorDir.Parent
	if parent == nil {
		return
	}
	child := m.cursorDir
	m.cursorDir = parent
	m.cursorIdx = 0
	for i, c := range parent.Children {
		if c == child {
			m.cursorIdx = i
			break
		}
	}
	m.scrollOffset = 0
	m.ensureCursorVisible()
	m.sess.Navigate(parent)
}

// jumpDiff moves the cursor to the next (or previous) row in the current
// directory whose status isn't "same" — SPEC.md §9's 'n'/'N'. It only
// considers rows already compared at some level; it wraps around.
func (m *Model) jumpDiff(forward bool) {
	n := len(m.cursorDir.Children)
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
		if isDiffering(m.cursorDir.Children[i]) {
			m.cursorIdx = i
			m.ensureCursorVisible()
			return
		}
	}
}

func isDiffering(n *tree.Node) bool {
	if n.Presence != diffmodel.Both {
		return true
	}
	if n.IsDir() {
		return n.Rollup == diffmodel.Differs || n.Rollup == diffmodel.CompareError
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
	h := m.height - headerHeight - detailsPanelHeight - statusBarHeight
	if h < 1 {
		h = 1
	}
	return h
}
