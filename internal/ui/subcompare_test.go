package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/pairtree"
	"github.com/m42cel/dirdiff/internal/session"
)

// movedModel is the case sub-compares exist for: a directory that was
// renamed, so it shows as left-only at the old path and right-only at
// the new one and nothing pairs the two.
func movedModel(t *testing.T) (Model, *session.Session) {
	t.Helper()
	m, sess := newTestModel(t)
	listSide(sess, diffmodel.Left, "", "old-name/")
	listSide(sess, diffmodel.Right, "", "new-name/")
	listSide(sess, diffmodel.Left, "old-name", "same.txt", "nested/")
	listSide(sess, diffmodel.Right, "new-name", "same.txt", "nested/")
	listSide(sess, diffmodel.Left, "old-name/nested", "deep.txt")
	listSide(sess, diffmodel.Right, "new-name/nested", "deep.txt")
	return m, sess
}

// selectRow puts the cursor on a row by name, so a test says which row
// it means rather than depending on where the sort happened to put it.
func selectRow(t *testing.T, m Model, name string) Model {
	t.Helper()
	for i, c := range m.visibleChildren() {
		if c.Name() == name {
			m.cursorIdx = i
			return m
		}
	}
	t.Fatalf("no row named %q among %d rows", name, len(m.visibleChildren()))
	return m
}

// subCompare runs the whole flow: s, choose the left directory, choose
// the right one.
func subCompare(t *testing.T, m Model, leftName, rightName string) Model {
	t.Helper()
	m = press(t, m, "s")
	m = selectRow(t, m, leftName)
	m = press(t, m, " ")
	m = selectRow(t, m, rightName)
	return press(t, m, " ")
}

// The whole flow (SPEC.md §4.9): s, choose one side, move the cursor,
// choose the other.
func TestChooseBothSidesAndPair(t *testing.T) {
	m, _ := movedModel(t)

	m = press(t, m, "s")
	if m.picking == nil || m.picking.side != diffmodel.Left {
		t.Fatal("s should start a selection, on the left side first")
	}

	m = selectRow(t, m, "old-name")
	m = press(t, m, " ")
	if m.picking == nil || m.picking.side != diffmodel.Right {
		t.Fatal("choosing the left directory should move on to the right side")
	}
	if m.picking.left == nil || m.picking.left.RelPath != "old-name" {
		t.Fatalf("left choice = %v; want old-name", m.picking.left)
	}

	m = selectRow(t, m, "new-name")
	m = press(t, m, " ")
	if m.picking != nil {
		t.Fatal("the selection should be over once both sides are chosen")
	}
	if len(m.stack) != 1 {
		t.Fatalf("view stack depth = %d; want 1 — the second choice should have pushed a sub-compare", len(m.stack))
	}
	if m.pairing == session.RootPairing {
		t.Fatal("still showing the root pairing after choosing both sides")
	}

	// Inside it, the two differently-named directories' contents pair up.
	var names []string
	for _, c := range m.visibleChildren() {
		names = append(names, c.Name())
		if c.Presence() != diffmodel.Both {
			t.Errorf("%s: Presence = %v; want Both — that's what the pairing is for", c.Name(), c.Presence())
		}
	}
	if strings.Join(names, ",") != "nested,same.txt" {
		t.Fatalf("rows = %v; want the paired contents", names)
	}
}

// The mode adds a key rather than rebinding any: →/Enter and ← navigate
// exactly as they do outside it, which is how you get to a directory
// that isn't in the listing you started from.
func TestNavigationWorksUnchangedWhileChoosing(t *testing.T) {
	m, _ := movedModel(t)

	m = press(t, m, "s")
	m = selectRow(t, m, "old-name")
	for _, key := range []string{"right", "enter"} {
		into := press(t, m, key)
		if into.cursorDir.Name() != "old-name" {
			t.Fatalf("%s while choosing left cursorDir at %q; want to have descended into old-name", key, into.cursorDir.Name())
		}
		if into.picking == nil || into.picking.left != nil {
			t.Fatalf("%s while choosing should navigate, not choose", key)
		}
	}

	// And the choice can then be made further down.
	m = press(t, m, "right")
	m = selectRow(t, m, "nested")
	m = press(t, m, " ")
	if m.picking.left == nil || m.picking.left.RelPath != "old-name/nested" {
		t.Fatalf("left choice = %v; want the directory navigated to", m.picking.left)
	}
}

func TestChoosingASideThatIsntThere(t *testing.T) {
	m, _ := movedModel(t)

	// old-name is left-only, so it can't be the right-hand side.
	m = press(t, m, "s")
	m = selectRow(t, m, "new-name")
	m = press(t, m, " ") // fine: new-name is right-only, but we want its left…
	if m.picking.left != nil {
		t.Fatal("a right-only row has no left side to choose")
	}
	if !strings.Contains(m.note, "left") {
		t.Errorf("note = %q; want it to say the row isn't on the left", m.note)
	}
	// The selection stays open, so the next candidate is one keypress away.
	if m.picking == nil || m.picking.side != diffmodel.Left {
		t.Fatal("a refused choice should leave the selection running")
	}

	// And the note lasts exactly until the next keypress.
	m = press(t, m, "down")
	if m.note != "" {
		t.Errorf("note = %q after another keypress; want it cleared", m.note)
	}
}

func TestChoosingAFileIsRefused(t *testing.T) {
	m, sess := newTestModel(t)
	listBoth(sess, "", "a.txt")

	m = press(t, m, "s")
	m = press(t, m, " ")
	if m.picking.left != nil {
		t.Fatal("a sub-compare pairs directories, not files")
	}
	if !strings.Contains(m.note, "directories") {
		t.Errorf("note = %q; want it to say why a file can't be chosen", m.note)
	}
}

// Esc abandons the selection and puts the view back where s was pressed,
// since navigating around to find a directory was incidental to an
// operation that didn't happen.
func TestCancellingRestoresWhereItStarted(t *testing.T) {
	for _, key := range []string{"esc", "s"} {
		m, _ := movedModel(t)
		m = selectRow(t, m, "new-name")
		startIdx := m.cursorIdx

		m = press(t, m, "s")
		m = selectRow(t, m, "old-name")
		m = press(t, m, " ")
		m = press(t, m, "right") // wander off into the chosen directory

		m = press(t, m, key)
		if m.picking != nil {
			t.Fatalf("%s should cancel the selection", key)
		}
		if m.cursorDir != m.root || m.cursorIdx != startIdx {
			t.Errorf("%s left the cursor at %q/%d; want it back where the selection started", key, m.cursorDir.Name(), m.cursorIdx)
		}
		if len(m.stack) != 0 {
			t.Errorf("%s opened a pairing", key)
		}
	}
}

// ← past a sub-compare's top row leaves it for whatever it was opened
// from, and the pairing is dropped on the way out.
func TestLeavingASubCompareReturnsAndClosesIt(t *testing.T) {
	m, sess := movedModel(t)

	m = selectRow(t, m, "new-name")
	m = subCompare(t, m, "old-name", "new-name")
	sub := m.pairing
	m = selectRow(t, m, "nested")
	m = press(t, m, "right") // into "nested"
	if m.cursorDir.Name() != "nested" {
		t.Fatalf("cursorDir = %q; want to be inside nested", m.cursorDir.Name())
	}

	m = press(t, m, "left") // back to the sub-compare's root
	m = press(t, m, "left") // up to its root-parent row
	if !m.atRootParent {
		t.Fatal("left at a sub-compare's root should go up to its pair row, not straight out")
	}
	m = press(t, m, "left") // and out

	if len(m.stack) != 0 || m.pairing != session.RootPairing {
		t.Fatalf("stack=%d pairing=%v; want to be back in the root pairing", len(m.stack), m.pairing)
	}
	if _, ok := sess.Pairing(sub); ok {
		t.Error("the sub-compare is still open after being left; it should be dropped")
	}
	// The origin goes on the stack, not wherever the hunt for the second
	// directory ended, so this lands where the whole operation started.
	if got := m.visibleChildren()[m.cursorIdx].Name(); got != "new-name" {
		t.Errorf("cursor is on %q; want new-name, the row the selection started from", got)
	}
}

// Leaving the pairing mid-choice would close the view the selection
// started in, so it's refused until the selection is finished or dropped.
func TestLeavingIsRefusedWhileChoosing(t *testing.T) {
	m, _ := movedModel(t)
	m = subCompare(t, m, "old-name", "new-name")
	sub := m.pairing

	m = press(t, m, "s")
	m = press(t, m, "left") // up to the pair row
	m = press(t, m, "left") // and would leave
	if m.pairing != sub {
		t.Fatal("left out of a sub-compare while choosing should be refused")
	}
	if m.note == "" {
		t.Error("a refused exit should say why")
	}

	// Cancelling first makes the way out work as usual.
	m = press(t, m, "esc")
	m = press(t, m, "left")
	m = press(t, m, "left")
	if m.pairing != session.RootPairing {
		t.Fatal("left should leave the sub-compare once the selection is cancelled")
	}
}

// The root pairing is the bottom of the stack: ← past its top row stays
// put rather than leaving nothing to show.
func TestLeavingTheRootPairingStaysPut(t *testing.T) {
	m, _ := movedModel(t)

	m = press(t, m, "left")
	m = press(t, m, "left")
	if !m.atRootParent || m.pairing != session.RootPairing {
		t.Fatal("the root pairing should stay on screen with nowhere further up to go")
	}
	if len(m.visibleChildren()) != 1 {
		t.Error("the row leading back down must stay visible")
	}
}

// Sub-compares nest: s from inside one opens another, and ← pops back
// to the one it was opened from rather than all the way out.
func TestSubComparesNest(t *testing.T) {
	m, _ := movedModel(t)

	m = subCompare(t, m, "old-name", "new-name")
	outer := m.pairing

	// Inside it, pair "nested" with itself — any two directories will do.
	m = subCompare(t, m, "nested", "nested")
	if len(m.stack) != 2 {
		t.Fatalf("stack depth = %d; want 2 — a sub-compare opened from inside a sub-compare", len(m.stack))
	}

	m = press(t, m, "left")
	m = press(t, m, "left")
	if m.pairing != outer {
		t.Fatalf("popped to %v; want the pairing it was opened from (%v), not the root", m.pairing, outer)
	}
	if len(m.stack) != 1 {
		t.Fatalf("stack depth = %d after one pop; want 1", len(m.stack))
	}
}

// Under a sub-compare the two panes stand at unrelated paths, so each
// title has to be that side's own real path.
func TestSubComparePaneTitlesShowEachSidesRealPath(t *testing.T) {
	m, sess := movedModel(t)
	m.width = 400

	m = subCompare(t, m, "old-name", "new-name")

	if got, want := m.paneTitle(diffmodel.Left), sess.LeftRoot+"/old-name"; got != want {
		t.Errorf("left pane title = %q; want %q", got, want)
	}
	if got, want := m.paneTitle(diffmodel.Right), sess.RightRoot+"/new-name"; got != want {
		t.Errorf("right pane title = %q; want %q", got, want)
	}

	// And the pair row above them names each side by its own name.
	m = press(t, m, "left")
	left, _, right := m.renderRootParentRow(60, 60)
	if !strings.Contains(left, "old-name") || !strings.Contains(right, "new-name") {
		t.Errorf("pair row = %q / %q; want the two directory names side by side", left, right)
	}
}

// The status bar has to say which side Space would take, and keep the
// first choice visible once it's off screen.
func TestStatusBarFollowsTheSelection(t *testing.T) {
	m, _ := movedModel(t)
	m.width = 400

	if got := m.whereLabel(); got != "" {
		t.Errorf("where label = %q at the root pairing with nothing being chosen; want nothing said", got)
	}

	m = press(t, m, "s")
	if got := m.whereLabel(); !strings.Contains(got, "LEFT") {
		t.Errorf("where label = %q; want it to say which side is being chosen", got)
	}

	m = selectRow(t, m, "old-name")
	m = press(t, m, " ")
	got := m.whereLabel()
	if !strings.Contains(got, "old-name") || !strings.Contains(got, "RIGHT") {
		t.Errorf("where label = %q; want the choice made and the side still to go", got)
	}

	m = selectRow(t, m, "new-name")
	m = press(t, m, " ")
	got = m.whereLabel()
	if !strings.Contains(got, "sub-compare") || !strings.Contains(got, "old-name ↔ new-name") {
		t.Errorf("where label = %q; want a breadcrumb naming the two paired directories", got)
	}
	if strings.Contains(got, "choose") {
		t.Error("the selection prompt should be gone once the pairing is open")
	}
}

// The already-chosen directory is marked where it's visible, and the
// side not being chosen fades — both only while a selection is running.
func TestChosenDirectoryIsMarkedAndTheOtherSideFades(t *testing.T) {
	// Two both-sided directories, so each row has a left cell and a right
	// cell to compare against each other.
	m, sess := newTestModel(t)
	listBoth(sess, "", "a/", "b/")

	row := func(m Model, name string) (string, string) {
		m = selectRow(t, m, name)
		n := m.visibleChildren()[m.cursorIdx]
		left, _, right := m.renderRowTriple(n, false, false, 40, 40)
		return left, right
	}

	if left, _ := row(m, "a"); strings.Contains(left, pickedGlyph) {
		t.Errorf("left = %q outside a selection; want no choice marker", left)
	}

	m = press(t, m, "s")
	m = selectRow(t, m, "a")
	m = press(t, m, " ")

	if left, _ := row(m, "a"); !strings.Contains(left, pickedGlyph) {
		t.Errorf("left = %q; want the chosen directory marked out", left)
	}

	// The fading has to be read off the style rather than the rendered
	// string: lipgloss strips styling when it renders without a TTY, as
	// it does under `go test`, so the escape codes never reach the output
	// here.
	rowOf := func(m Model, name string) *pairtree.Node {
		m = selectRow(t, m, name)
		return m.visibleChildren()[m.cursorIdx]
	}
	base := lipgloss.NewStyle()

	// The left column is no longer the one being chosen from, so a row
	// that isn't the choice fades there — but the choice itself doesn't,
	// since the whole point of marking it is that it stays findable.
	if st, _ := m.sideStyle(rowOf(m, "b"), diffmodel.Left, base); !st.GetFaint() {
		t.Error("the side not being chosen should fade")
	}
	if st, _ := m.sideStyle(rowOf(m, "b"), diffmodel.Right, base); st.GetFaint() {
		t.Error("the side being chosen should stay undimmed")
	}
	if st, _ := m.sideStyle(rowOf(m, "a"), diffmodel.Left, base); st.GetFaint() {
		t.Error("the already-chosen directory should be marked out, not faded with its column")
	}

	// And none of it applies once the selection is over.
	m = press(t, m, "esc")
	if st, prefix := m.sideStyle(rowOf(m, "a"), diffmodel.Left, base); st.GetFaint() || prefix != "" {
		t.Error("no fading or choice marker should survive the selection")
	}
}
