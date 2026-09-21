package ui

import (
	"strings"
	"testing"

	"github.com/m42cel/dirdiff/internal/diffmodel"
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

// markPair marks the two named rows and pairs them.
func markPair(t *testing.T, m Model, leftName, rightName string) Model {
	t.Helper()
	m = selectRow(t, m, leftName)
	m = press(t, m, "[")
	m = selectRow(t, m, rightName)
	m = press(t, m, "]")
	return press(t, m, "p")
}

// The whole flow (SPEC.md §4.9): mark one side, move the cursor, mark
// the other, pair them.
func TestMarkBothSidesAndPair(t *testing.T) {
	m, _ := movedModel(t)

	m = selectRow(t, m, "old-name")
	m = press(t, m, "[")
	m = selectRow(t, m, "new-name")
	m = press(t, m, "]")
	if m.markLeft == nil || m.markLeft.RelPath != "old-name" {
		t.Fatalf("left mark = %v; want old-name", m.markLeft)
	}
	if m.markRight == nil || m.markRight.RelPath != "new-name" {
		t.Fatalf("right mark = %v; want new-name", m.markRight)
	}

	m = press(t, m, "p")
	if len(m.stack) != 1 {
		t.Fatalf("view stack depth = %d; want 1 — p should have pushed a sub-compare", len(m.stack))
	}
	if m.pairing == session.RootPairing {
		t.Fatal("still showing the root pairing after p")
	}
	if m.markLeft != nil || m.markRight != nil {
		t.Error("the marks should be cleared once they've been paired")
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

// A mark points at a side node, not a row, so it survives navigating
// anywhere at all between the two keypresses.
func TestMarksSurviveNavigation(t *testing.T) {
	m, _ := movedModel(t)

	m = selectRow(t, m, "old-name")
	m = press(t, m, "[")     // mark old-name on the left
	m = press(t, m, "right") // descend into it
	m = press(t, m, "left")  // and back out
	m = press(t, m, "left")  // up past the roots
	if m.markLeft == nil || m.markLeft.RelPath != "old-name" {
		t.Fatalf("left mark = %v after navigating away and back; want it kept", m.markLeft)
	}
}

func TestMarkingASideThatIsntThere(t *testing.T) {
	m, _ := movedModel(t)

	// old-name is left-only, so it has no right side to mark.
	m = selectRow(t, m, "old-name")
	m = press(t, m, "]")
	if m.markRight != nil {
		t.Fatalf("right mark = %v; want none — the row doesn't exist on the right", m.markRight)
	}
	if !strings.Contains(m.note, "right") {
		t.Errorf("note = %q; want it to say the row isn't on the right", m.note)
	}

	// And the note lasts exactly until the next keypress.
	m = press(t, m, "down")
	if m.note != "" {
		t.Errorf("note = %q after another keypress; want it cleared", m.note)
	}
}

func TestPairingWithoutTwoMarksIsANoOp(t *testing.T) {
	m, _ := movedModel(t)

	m = selectRow(t, m, "old-name")
	m = press(t, m, "[")
	m = press(t, m, "p")
	if len(m.stack) != 0 {
		t.Fatal("p opened a pairing with only one mark set")
	}
	if m.note == "" {
		t.Error("p with only one mark should say why it did nothing")
	}
}

func TestMarkingAFileIsRefused(t *testing.T) {
	m, sess := newTestModel(t)
	listBoth(sess, "", "a.txt")

	m = press(t, m, "[")
	if m.markLeft != nil {
		t.Fatalf("left mark = %v; want none — a sub-compare pairs directories", m.markLeft)
	}
	if !strings.Contains(m.note, "directories") {
		t.Errorf("note = %q; want it to say why a file can't be marked", m.note)
	}
}

// ← past a sub-compare's top row leaves it for whatever it was opened
// from, and the pairing is dropped on the way out.
func TestLeavingASubCompareReturnsAndClosesIt(t *testing.T) {
	m, sess := movedModel(t)

	m = markPair(t, m, "old-name", "new-name")
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
	// The cursor is back where it was, on the row that was selected when
	// the sub-compare was opened.
	if got := m.visibleChildren()[m.cursorIdx].Name(); got != "new-name" {
		t.Errorf("cursor is on %q; want new-name, the row it was left on", got)
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

// Sub-compares nest: p from inside one pushes another, and ← pops back
// to the one it was opened from rather than all the way out.
func TestSubComparesNest(t *testing.T) {
	m, _ := movedModel(t)

	m = markPair(t, m, "old-name", "new-name")
	outer := m.pairing

	// Inside it, pair "nested" with itself — any two directories will do.
	m = markPair(t, m, "nested", "nested")
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

	m = markPair(t, m, "old-name", "new-name")

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

// The status bar has to say when the view isn't the ordinary one, and
// what's waiting to be paired.
func TestStatusBarShowsMarksAndSubCompare(t *testing.T) {
	m, _ := movedModel(t)
	m.width = 400

	if got := m.whereLabel(); got != "" {
		t.Errorf("where label = %q at the root pairing with nothing marked; want nothing said", got)
	}

	m = selectRow(t, m, "old-name")
	m = press(t, m, "[")
	if got := m.whereLabel(); !strings.Contains(got, "old-name") || !strings.Contains(got, "p to pair") {
		t.Errorf("where label = %q; want the pending mark and how to use it", got)
	}

	m = selectRow(t, m, "new-name")
	m = press(t, m, "]")
	m = press(t, m, "p")
	got := m.whereLabel()
	if !strings.Contains(got, "sub-compare") || !strings.Contains(got, "old-name ↔ new-name") {
		t.Errorf("where label = %q; want a breadcrumb naming the two paired directories", got)
	}
	if strings.Contains(got, "p to pair") {
		t.Error("the marks should be gone from the status bar once they're paired")
	}
}
