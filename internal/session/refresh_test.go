package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/pairtree"
	"github.com/m42cel/dirdiff/internal/scan"
)

func mustNode(t *testing.T, s *Session, relPath string) *pairtree.Node {
	t.Helper()
	n, ok := s.Node(relPath)
	if !ok {
		t.Fatalf("no row at %q", relPath)
	}
	return n
}

// assertNothingPending checks that no pending count was left behind: a
// refresh uncounts the jobs of what it discards, and a stray count would
// leave a spinner running forever.
func assertNothingPending(t *testing.T, s *Session) {
	t.Helper()
	root := s.Tree()
	l, r := root.PendingListing()
	sl, sr := root.PendingStat()
	if l != 0 || r != 0 || sl != 0 || sr != 0 || root.PendingCompare != 0 {
		t.Fatalf("pending at the root: listing %d/%d, stat %d/%d, compare %d; want all 0", l, r, sl, sr, root.PendingCompare)
	}
}

func TestRefreshPicksUpADeletedSubtree(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	for _, root := range []string{left, right} {
		mustMkdir(t, filepath.Join(root, "sub", "deep"))
		mustWrite(t, filepath.Join(root, "sub", "deep", "f.txt"), "x")
	}
	s := New(left, right, 2, 2, diffmodel.SizeMtime)
	defer s.Close()
	settle(t, s)
	before := mustNode(t, s, "sub")

	if err := os.RemoveAll(filepath.Join(right, "sub")); err != nil {
		t.Fatal(err)
	}
	s.Refresh(RootPairing, before)
	settle(t, s)

	sub := mustNode(t, s, "sub")
	if sub == before || sub.Presence() != diffmodel.LeftOnly {
		t.Fatalf("sub presence = %v (replaced: %v); want a new, left-only row", sub.Presence(), sub != before)
	}
	if _, right := s.Tree().SideTotals(); right.Dirs != 0 || right.Files != 0 {
		t.Fatalf("right totals = %+v; want nothing left on the right", right)
	}
	if f := mustNode(t, s, "sub/deep/f.txt"); f.Presence() != diffmodel.LeftOnly {
		t.Fatalf("sub/deep/f.txt presence = %v; want LeftOnly", f.Presence())
	}
	assertNothingPending(t, s)
}

// A refreshed entry is compared at --level only (SPEC.md §5.5): the
// content comparison that was run on it by hand is not repeated.
func TestRefreshComparesAtTheStartupLevelOnly(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(left, "f.txt"), "aaa")
	mustWrite(t, filepath.Join(right, "f.txt"), "bbb")
	s := New(left, right, 1, 1, diffmodel.SizeMtime)
	defer s.Close()
	settle(t, s)
	f := mustNode(t, s, "f.txt")
	s.TriggerCompare(RootPairing, f, diffmodel.Content, false)
	settle(t, s)
	if f.Level != diffmodel.Content {
		t.Fatalf("setup: level = %v; want Content", f.Level)
	}

	mustWrite(t, filepath.Join(right, "f.txt"), "bbbb")
	s.Refresh(RootPairing, f)
	settle(t, s)

	f = mustNode(t, s, "f.txt")
	if f.Level != diffmodel.SizeMtime || f.Result != diffmodel.Differs {
		t.Fatalf("after refresh: level %v, result %v; want a fresh metadata verdict (Differs) and no content compare", f.Level, f.Result)
	}
	if right := f.Right; right.Size != 4 {
		t.Fatalf("right size = %d; want the re-read 4", right.Size)
	}
	assertNothingPending(t, s)
}

func TestRefreshUnderLevelNoneOnlyLists(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	for _, root := range []string{left, right} {
		mustMkdir(t, filepath.Join(root, "sub"))
		mustWrite(t, filepath.Join(root, "sub", "f.txt"), "x")
	}
	s := New(left, right, 1, 1, diffmodel.NotCompared)
	defer s.Close()
	settle(t, s)
	s.TriggerCompare(RootPairing, s.Tree(), diffmodel.Content, true)
	settle(t, s)

	c := new(counts)
	s.Refresh(RootPairing, mustNode(t, s, "sub"))
	c.settle(t, s)

	if c.stats != 0 || c.compares != 0 {
		t.Fatalf("refresh read %d stats and %d contents; want listing only under --level=none", c.stats, c.compares)
	}
	if f := mustNode(t, s, "sub/f.txt"); f.Level != diffmodel.NotCompared {
		t.Fatalf("sub/f.txt level = %v; want the earlier recursive trigger not carried over", f.Level)
	}
}

// Refreshing a pairing's root re-lists it in place, and reaches the
// whole subtree level by level through the directories it keeps.
func TestRefreshOfTheRootReachesTheWholeSubtree(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	for _, root := range []string{left, right} {
		mustMkdir(t, filepath.Join(root, "a", "b"))
		mustWrite(t, filepath.Join(root, "a", "b", "old.txt"), "x")
	}
	s := New(left, right, 2, 2, diffmodel.SizeMtime)
	defer s.Close()
	settle(t, s)
	a := mustNode(t, s, "a")

	mustWrite(t, filepath.Join(left, "a", "b", "new.txt"), "y")
	if err := os.Remove(filepath.Join(right, "a", "b", "old.txt")); err != nil {
		t.Fatal(err)
	}
	s.Refresh(RootPairing, s.Tree())
	settle(t, s)

	if mustNode(t, s, "a") != a {
		t.Fatal("a kept directory must keep its row")
	}
	if n := mustNode(t, s, "a/b/new.txt"); n.Presence() != diffmodel.LeftOnly {
		t.Fatalf("a/b/new.txt presence = %v; want LeftOnly", n.Presence())
	}
	if n := mustNode(t, s, "a/b/old.txt"); n.Presence() != diffmodel.LeftOnly {
		t.Fatalf("a/b/old.txt presence = %v; want LeftOnly", n.Presence())
	}
	assertNothingPending(t, s)
}

// Refreshing one row reads only that row: a sibling that changed too is
// left as it was until it's refreshed itself.
func TestRefreshOfARowLeavesItsSiblingsAlone(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	for _, root := range []string{left, right} {
		mustWrite(t, filepath.Join(root, "a.txt"), "x")
		mustWrite(t, filepath.Join(root, "b.txt"), "x")
	}
	s := New(left, right, 1, 1, diffmodel.NotCompared)
	defer s.Close()
	settle(t, s)

	if err := os.Remove(filepath.Join(right, "b.txt")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(right, "c.txt"), "x")
	s.Refresh(RootPairing, mustNode(t, s, "a.txt"))
	settle(t, s)

	if b := mustNode(t, s, "b.txt"); b.Presence() != diffmodel.Both {
		t.Fatalf("b.txt presence = %v; want it untouched", b.Presence())
	}
	if _, ok := s.Node("c.txt"); ok {
		t.Fatal("c.txt appeared; a row refresh must not pick up its siblings")
	}
}

// A result that was in flight when its entry was re-read describes the
// file as it was, so it must not land.
func TestRefreshDropsResultsForWhatItReRead(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(left, "f.txt"), "x")
	mustWrite(t, filepath.Join(right, "f.txt"), "x")
	s := New(left, right, 1, 1, diffmodel.SizeMtime)
	defer s.Close()
	settle(t, s)
	f := mustNode(t, s, "f.txt")
	staleStat, staleRow := f.Left.Gen, f.Gen

	s.Refresh(RootPairing, f)
	settle(t, s)

	s.OnStatResult(scan.StatResult{Side: diffmodel.Left, RelPath: "f.txt", Gen: staleStat, Size: 999})
	s.OnCompareResult(CompareResult{Pairing: RootPairing, CompareOutcome: scan.CompareOutcome{
		RelPath: "f.txt", Gen: staleRow, Level: diffmodel.Content, Result: diffmodel.Differs,
	}})

	if f.Left.Size == 999 || f.Level == diffmodel.Content {
		t.Fatalf("stale results landed: size %d, level %v", f.Left.Size, f.Level)
	}
	assertNothingPending(t, s)
}

// The side trees are shared, so a refresh shows in every open pairing
// over what it re-read — here a sub-compare and the root pairing at once.
func TestRefreshReachesEveryOpenPairing(t *testing.T) {
	left, right := movedTree(t)
	s := New(left, right, 2, 2, diffmodel.SizeMtime)
	defer s.Close()
	settle(t, s)
	id, p := openPairing(t, s, "old-name", "new-name")

	mustWrite(t, filepath.Join(left, "old-name", "added.txt"), "z")
	s.Refresh(id, p.Root)
	settle(t, s)

	var inSub bool
	for _, c := range p.Root.Children {
		if c.Name() == "added.txt" {
			inSub = c.Presence() == diffmodel.LeftOnly
		}
	}
	if !inSub {
		t.Fatal("added.txt missing from the sub-compare it was refreshed in")
	}
	if n := mustNode(t, s, "old-name/added.txt"); n.Presence() != diffmodel.LeftOnly {
		t.Fatalf("root pairing: old-name/added.txt presence = %v; want LeftOnly", n.Presence())
	}
	assertNothingPending(t, s)
}
