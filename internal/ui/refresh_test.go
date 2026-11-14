package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/scan"
	"github.com/m42cel/dirdiff/internal/session"
)

// newDiskModel wires a model to a real session over two directories
// build fills in identically, and lets the background scan finish.
func newDiskModel(t *testing.T, build func(root string)) (m Model, left, right string) {
	t.Helper()
	left, right = filepath.Join(t.TempDir(), "l"), filepath.Join(t.TempDir(), "r")
	for _, root := range []string{left, right} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		build(root)
	}
	sess := session.New(left, right, 1, 1, diffmodel.SizeMtime)
	t.Cleanup(sess.Close)
	m = New(sess)
	m.width, m.height = 100, 30
	return settleModel(t, m), left, right
}

// settleModel feeds every background result through Update, the way the
// Bubble Tea loop does, until nothing is outstanding or in a channel.
func settleModel(t *testing.T, m Model) Model {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		var msg any
		select {
		case r := <-m.sess.ListResults():
			msg = listResultsMsg{[]scan.ListResult{r}}
		case r := <-m.sess.StatResults():
			msg = statResultsMsg{[]scan.StatResult{r}}
		case r := <-m.sess.CompareResults():
			msg = compareResultsMsg{[]session.CompareResult{r}}
		case <-time.After(100 * time.Millisecond):
			if !m.sess.HasPendingWork() {
				return m
			}
			continue
		case <-deadline:
			t.Fatal("timed out waiting for the session to settle")
		}
		tm, _ := m.Update(msg)
		m = tm.(Model)
	}
}

func mustRemove(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}
}

func write(path string) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte("x"), 0o644)
}

func cursorName(m Model) string {
	visible := m.visibleChildren()
	if m.cursorIdx >= len(visible) {
		return ""
	}
	return visible[m.cursorIdx].Name()
}

// The cursor stays on its row when a refresh removes rows above it,
// rather than keeping an index that now points at a different row.
func TestRefreshKeepsTheCursorOnItsRow(t *testing.T) {
	m, left, right := newDiskModel(t, func(root string) {
		for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
			write(filepath.Join(root, name))
		}
	})
	m = press(t, m, "down")
	if cursorName(m) != "b.txt" {
		t.Fatalf("setup: cursor on %q", cursorName(m))
	}

	mustRemove(t, filepath.Join(left, "a.txt"), filepath.Join(right, "a.txt"))
	m = settleModel(t, press(t, m, "U"))

	if len(m.visibleChildren()) != 2 || cursorName(m) != "b.txt" {
		t.Fatalf("cursor on %q among %d rows; want still on b.txt", cursorName(m), len(m.visibleChildren()))
	}
}

// Standing in a directory that a refresh replaces — here because it's
// now missing on one side — the view follows it to its new row.
func TestRefreshFollowsAReplacedCurrentDirectory(t *testing.T) {
	m, _, right := newDiskModel(t, func(root string) {
		write(filepath.Join(root, "d", "f.txt"))
	})
	m = press(t, m, "enter")
	old := m.cursorDir

	mustRemove(t, filepath.Join(right, "d"))
	m = settleModel(t, press(t, m, "U"))

	if m.cursorDir == old || m.cursorDir.PairRel != "d" || m.cursorDir.Presence() != diffmodel.LeftOnly {
		t.Fatalf("standing in %q (%v, replaced: %v); want the new left-only row for d", m.cursorDir.PairRel, m.cursorDir.Presence(), m.cursorDir != old)
	}
	if len(m.visibleChildren()) != 1 {
		t.Fatalf("%d rows in d; want its one file", len(m.visibleChildren()))
	}
}

// A directory gone from both sides leaves the view standing in its
// nearest surviving ancestor, with the cursor where the old path went.
func TestRefreshMovesUpOutOfADeletedDirectory(t *testing.T) {
	m, left, right := newDiskModel(t, func(root string) {
		write(filepath.Join(root, "a", "b", "f.txt"))
		write(filepath.Join(root, "a", "keep.txt"))
	})
	m = press(t, m, "enter")
	m = press(t, m, "enter")
	if m.cursorDir.PairRel != "a/b" {
		t.Fatalf("setup: standing in %q", m.cursorDir.PairRel)
	}

	mustRemove(t, filepath.Join(left, "a", "b"), filepath.Join(right, "a", "b"))
	m = settleModel(t, press(t, m, "U"))

	if m.cursorDir.PairRel != "a" {
		t.Fatalf("standing in %q; want a, the nearest directory that still exists", m.cursorDir.PairRel)
	}
	if cursorName(m) != "keep.txt" {
		t.Fatalf("cursor on %q; want the only row left", cursorName(m))
	}
}

// 'u' refreshes the selected row only; 'U' the whole directory.
func TestRefreshRowLeavesSiblingsUntilTheDirectoryIsRefreshed(t *testing.T) {
	m, _, right := newDiskModel(t, func(root string) {
		write(filepath.Join(root, "a.txt"))
		write(filepath.Join(root, "b.txt"))
	})
	write(filepath.Join(right, "c.txt"))

	m = settleModel(t, press(t, m, "u"))
	if len(m.visibleChildren()) != 2 {
		t.Fatalf("%d rows after u; want c.txt not picked up by a row refresh", len(m.visibleChildren()))
	}

	m = settleModel(t, press(t, m, "U"))
	if len(m.visibleChildren()) != 3 {
		t.Fatalf("%d rows after U; want c.txt picked up", len(m.visibleChildren()))
	}
}
