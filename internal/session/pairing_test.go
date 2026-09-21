package session

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/pairtree"
	"github.com/m42cel/dirdiff/internal/sidetree"
)

// sideNode is the sidetree node a test wants to pair, addressed the way
// a mark will be in the UI: by side and path.
func sideNode(t *testing.T, s *Session, sd diffmodel.Side, relPath string) *sidetree.Node {
	t.Helper()
	n, ok := s.sides[sd].tree.Index[relPath]
	if !ok {
		t.Fatalf("no %v-side node at %q", sd, relPath)
	}
	return n
}

func openPairing(t *testing.T, s *Session, leftRel, rightRel string) (PairingID, *pairtree.Pairing) {
	t.Helper()
	id, err := s.OpenPairing(sideNode(t, s, diffmodel.Left, leftRel), sideNode(t, s, diffmodel.Right, rightRel))
	if err != nil {
		t.Fatalf("OpenPairing: %v", err)
	}
	p, ok := s.Pairing(id)
	if !ok {
		t.Fatal("the pairing that was just opened isn't open")
	}
	return id, p
}

// The motivating case (SPEC.md §4.9): a directory moved, so it shows as
// one-sided at both the old path and the new one, and the only way to
// ask whether they're the same is to pair them directly.
func movedTree(t *testing.T) (left, right string) {
	t.Helper()
	left, right = t.TempDir(), t.TempDir()
	mustMkdir(t, filepath.Join(left, "old-name", "nested"))
	mustMkdir(t, filepath.Join(right, "new-name", "nested"))
	mustWrite(t, filepath.Join(left, "old-name", "same.txt"), "hello")
	mustWrite(t, filepath.Join(right, "new-name", "same.txt"), "hello")
	mustWrite(t, filepath.Join(left, "old-name", "nested", "deep.txt"), "aaa")
	mustWrite(t, filepath.Join(right, "new-name", "nested", "deep.txt"), "bbb")
	return left, right
}

// Opening a pairing over subtrees the ambient scan already covered
// enqueues no listing at all: the sides are shared, so all that's built
// is the matching between them.
func TestOpenPairingOverScannedSubtreesEnqueuesNoListing(t *testing.T) {
	left, right := movedTree(t)
	s := New(left, right, 2, 2, diffmodel.NotCompared)
	defer s.Close()

	settle(t, s)

	var c counts
	_, p := openPairing(t, s, "old-name", "new-name")

	if got := s.Stats().ListPending; got != 0 {
		t.Errorf("ListPending = %d right after opening a pairing over scanned subtrees; want 0", got)
	}
	c.drain(t, s)
	if c.lists != 0 {
		t.Errorf("%d further listings ran; want none — both sides were already listed", c.lists)
	}

	// And the matching is there in full, without a single new read.
	deep, ok := p.Row("nested/deep.txt")
	if !ok {
		t.Fatal("the paired subtree's nested row is missing; the merge stopped at the root")
	}
	if deep.Presence() != diffmodel.Both {
		t.Errorf("nested/deep.txt Presence = %v; want Both — that's the whole point of the pairing", deep.Presence())
	}
	if same, ok := p.Row("same.txt"); !ok || same.Presence() != diffmodel.Both {
		t.Error("same.txt should pair up across the two differently-named directories")
	}
}

// Metadata is shared, so a pairing opened after the scan settled starts
// out already knowing what its rows' verdicts are.
func TestOpenPairingInheritsMetadataVerdicts(t *testing.T) {
	left, right := movedTree(t)
	s := New(left, right, 2, 2, diffmodel.SizeMtime)
	defer s.Close()

	settle(t, s)

	var c counts
	_, p := openPairing(t, s, "old-name", "new-name")
	c.drain(t, s)

	deep, _ := p.Row("nested/deep.txt")
	if deep.Result != diffmodel.Differs || deep.Level != diffmodel.SizeMtime {
		t.Fatalf("nested/deep.txt = %v at %v; want a metadata verdict inherited for free", deep.Result, deep.Level)
	}
	if c.stats != 0 {
		t.Errorf("%d metadata reads ran for the new pairing; want none — they're shared", c.stats)
	}
}

// A pairing opened while one of its sides is still being listed has to
// fill in as the listing lands: Merge fires from OnListResult, not only
// at open time.
func TestOpenPairingMidListingFillsIn(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustMkdir(t, filepath.Join(left, "old-name"))
	mustMkdir(t, filepath.Join(right, "new-name"))
	for i := 0; i < 20; i++ {
		mustMkdir(t, filepath.Join(left, "old-name", fmt.Sprintf("d%d", i)))
		mustMkdir(t, filepath.Join(right, "new-name", fmt.Sprintf("d%d", i)))
		mustWrite(t, filepath.Join(left, "old-name", fmt.Sprintf("d%d", i), "f.txt"), "x")
		mustWrite(t, filepath.Join(right, "new-name", fmt.Sprintf("d%d", i), "f.txt"), "x")
	}

	s := New(left, right, 1, 1, diffmodel.NotCompared)
	defer s.Close()

	// Stop as soon as both roots are listed: the two subtrees underneath
	// are nowhere near done with a single listing worker.
	pump(t, s, 5*time.Second, func() bool { return s.Tree().Listed() })

	_, p := openPairing(t, s, "old-name", "new-name")

	pump(t, s, 10*time.Second, func() bool {
		_, ok := p.Row("d19/f.txt")
		return ok
	})
	if row, ok := p.Row("d19/f.txt"); !ok || row.Presence() != diffmodel.Both {
		t.Fatal("the deepest row never appeared; the pairing didn't follow the listing in")
	}
}

// A content verdict is a statement about a pair, so it belongs to the
// pairing that asked for it and to no other.
func TestContentVerdictsDoNotLeakBetweenPairings(t *testing.T) {
	left, right := movedTree(t)
	s := New(left, right, 2, 2, diffmodel.NotCompared)
	defer s.Close()

	settle(t, s)

	idA, pA := openPairing(t, s, "old-name", "new-name")
	_, pB := openPairing(t, s, "old-name", "new-name")

	rowA, _ := pA.Row("same.txt")
	rowB, _ := pB.Row("same.txt")
	s.TriggerCompare(idA, rowA, diffmodel.Checksum, false)

	pump(t, s, 5*time.Second, func() bool { return rowA.Level == diffmodel.Checksum })
	drainPending(t, s)

	if rowA.Result != diffmodel.Same {
		t.Fatalf("the compared pairing's row = %v; want Same", rowA.Result)
	}
	// Its metadata is shared and does reach the other pairing — that's
	// the point of reading it per side — but the content verdict, which
	// is a statement about a pair, does not.
	if rowB.Level == diffmodel.Checksum {
		t.Fatal("the other pairing's row got the content verdict too; a byte-for-byte result belongs to one pairing")
	}
}

// Closing a pairing retires its queued content jobs and nothing else:
// the shared metadata work of other pairings keeps running, and a result
// that was already in flight is dropped on arrival rather than landing
// in a tree that no longer exists.
func TestClosePairingDropsItsWorkAndItsLateResults(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustMkdir(t, filepath.Join(left, "old-name"))
	mustMkdir(t, filepath.Join(right, "new-name"))
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		mustWrite(t, filepath.Join(left, "old-name", name), "identical")
		mustWrite(t, filepath.Join(right, "new-name", name), "identical")
	}

	s := New(left, right, 2, 1, diffmodel.NotCompared)
	defer s.Close()

	settle(t, s)

	id, p := openPairing(t, s, "old-name", "new-name")
	// Measure everything first, so the content trigger that follows has
	// both sizes for every row and creates all 30 jobs on the spot —
	// leaving the queue holding this pairing's content work and nothing
	// else at the moment it's closed.
	s.TriggerCompare(id, p.Root, diffmodel.SizeMtime, true)
	settle(t, s)

	s.TriggerCompare(id, p.Root, diffmodel.Checksum, true)
	if p.Root.PendingCompare == 0 {
		t.Fatal("no content jobs queued; there would be nothing for the close to drop")
	}

	s.ClosePairing(id)
	if _, ok := s.Pairing(id); ok {
		t.Fatal("the pairing is still open after being closed")
	}
	if got := s.Stats().CmpPending; got != 0 {
		t.Fatalf("CmpPending = %d after closing the pairing; want its queued jobs dropped", got)
	}

	// Whatever was already in flight still reports back; it must be
	// dropped rather than applied.
	drainPending(t, s)
	drainPending(t, s)
	if got := s.Stats().CmpActive; got != 0 {
		t.Fatalf("CmpActive = %d; want the in-flight jobs to have finished", got)
	}
}

// Closing one pairing must not touch another's queued work, nor the
// shared metadata jobs — those are keyed by side, not by pairing.
func TestClosePairingLeavesOtherWorkAlone(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustMkdir(t, filepath.Join(left, "old-name"))
	mustMkdir(t, filepath.Join(right, "new-name"))
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		mustWrite(t, filepath.Join(left, "old-name", name), "x")
		mustWrite(t, filepath.Join(right, "new-name", name), "x")
	}

	s := New(left, right, 2, 1, diffmodel.NotCompared)
	defer s.Close()

	settle(t, s)

	idA, pA := openPairing(t, s, "old-name", "new-name")
	idB, pB := openPairing(t, s, "old-name", "new-name")
	s.TriggerCompare(idA, pA.Root, diffmodel.SizeMtime, true)
	s.TriggerCompare(idB, pB.Root, diffmodel.SizeMtime, true)

	pendingBefore := s.Stats().CmpPending
	if pendingBefore == 0 {
		t.Fatal("no metadata work queued; the test needs some to survive the close")
	}
	s.ClosePairing(idA)

	// Metadata jobs are keyed by side, not by pairing, so closing one
	// pairing drops none of them.
	if got := s.Stats().CmpPending; got != pendingBefore {
		t.Fatalf("CmpPending = %d after closing one pairing; want the %d shared metadata jobs untouched", got, pendingBefore)
	}
	if _, ok := s.Pairing(idB); !ok {
		t.Fatal("the other pairing was closed too")
	}
}

func TestOpenPairingRejectsNonDirectories(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(left, "f.txt"), "x")
	mustWrite(t, filepath.Join(right, "f.txt"), "x")

	s := New(left, right, 2, 2, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree().Listed() })

	file := sideNode(t, s, diffmodel.Left, "f.txt")
	if _, err := s.OpenPairing(file, s.sides[diffmodel.Right].tree.Root); err == nil {
		t.Error("pairing a file with a directory should be refused")
	}
	if _, err := s.OpenPairing(nil, nil); err == nil {
		t.Error("pairing nothing with nothing should be refused")
	}
}

func TestClosingTheRootPairingIsRefused(t *testing.T) {
	s := New(t.TempDir(), t.TempDir(), 1, 1, diffmodel.NotCompared)
	defer s.Close()

	s.ClosePairing(RootPairing)
	if _, ok := s.Pairing(RootPairing); !ok {
		t.Fatal("the root pairing was closed; there would be nothing left to show")
	}
}
