package ui

import (
	"testing"

	"github.com/m42cel/dirdiff/internal/diffmodel"
)

// A trigger raises the pending counts synchronously, before any result
// is pumped back in — which is what lets these tests read off exactly
// which rows a keypress reached.
func pendingRows(m Model) map[string]bool {
	out := map[string]bool{}
	for _, c := range m.cursorDir.Children {
		out[c.Name()] = c.ExaminePending()
	}
	return out
}

// 'c' targets the row under the cursor; 'C' targets the directory being
// stood in, whatever the cursor is on (SPEC.md §5.2).
func TestCompareKeysTargetRowAndDirectory(t *testing.T) {
	m, sess := newTestModel(t)
	listBoth(sess, "", "a.txt", "b.txt")

	m = press(t, m, "c")
	if got := pendingRows(m); !got["a.txt"] || got["b.txt"] {
		t.Fatalf("after c on the first row: %v; want only a.txt examined", got)
	}

	m.cursorIdx = 1
	m = press(t, m, "c")
	if got := pendingRows(m); !got["b.txt"] {
		t.Fatalf("after c on the second row: %v; want b.txt examined too", got)
	}
}

func TestCompareDirTargetsEveryRow(t *testing.T) {
	m, sess := newTestModel(t)
	listBoth(sess, "", "a.txt", "b.txt")

	m = press(t, m, "C")
	if got := pendingRows(m); !got["a.txt"] || !got["b.txt"] {
		t.Fatalf("after C: %v; want every row of the current directory examined", got)
	}
}

// The filter is a view concern: 'C' applies to the whole working
// directory, including rows it's hiding — while 'c', which acts on a
// row, has no row to act on.
func TestCompareDirIgnoresTheFilterAndCompareRowHasNothingToDo(t *testing.T) {
	m, sess := newTestModel(t)
	listBoth(sess, "", "a.txt")
	m.filter = setOf(FilterLeftOnly) // nothing here is left-only
	if len(m.visibleChildren()) != 0 {
		t.Fatal("the filter should be hiding every row for this test to mean anything")
	}

	m = press(t, m, "c")
	if got := pendingRows(m); got["a.txt"] {
		t.Fatalf("after c with no visible row: %v; want nothing examined", got)
	}

	m = press(t, m, "C")
	if got := pendingRows(m); !got["a.txt"] {
		t.Fatalf("after C with every row filtered out: %v; want the hidden row examined anyway", got)
	}
}

// Above the root the only row is the root pair itself (SPEC.md §4.3.1),
// so 'c' there compares the roots — recursively, by default, which is
// how the whole tree gets examined from a single keypress.
func TestCompareAtRootParentTargetsTheRoots(t *testing.T) {
	m, sess := newTestModel(t)
	listBoth(sess, "", "a.txt")

	m = press(t, m, "left")
	if !m.atRootParent {
		t.Fatal("left at the root should go up to the root-pair level")
	}
	m = press(t, m, "c")

	if !sess.Tree().ExaminePending() {
		t.Fatal("c above the roots examined nothing; want the root pair compared")
	}
}

// Level and recursive are persistent settings, not one-shot flags: 'c'
// runs whatever is currently selected and resets neither (SPEC.md §5.2).
func TestCompareKeysConsumeNeitherSetting(t *testing.T) {
	m, sess := newTestModel(t)
	listBoth(sess, "", "a.txt")

	m = press(t, m, "l") // metadata -> content
	m = press(t, m, "r") // recursive on -> off
	for _, key := range []string{"c", "C"} {
		m = press(t, m, key)
		if m.compareLevel != diffmodel.Checksum || m.recursive {
			t.Fatalf("after %s: level=%v recursive=%v; want content/false to survive the trigger",
				key, m.compareLevel, m.recursive)
		}
	}
}
