package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
)

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pump simulates the UI's Update loop: it's the only goroutine allowed to
// mutate the tree, draining result channels into it until cond is true.
func pump(t *testing.T, s *Session, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if cond() {
			return
		}
		select {
		case r := <-s.ListResults():
			s.OnListResult(r)
		case r := <-s.CompareResults():
			s.OnCompareResult(r)
		case <-deadline:
			t.Fatal("timed out waiting for condition")
		}
	}
}

func TestBFSEventuallyListsWholeTree(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	deep := filepath.Join("a", "b", "c")
	mustMkdir(t, filepath.Join(left, deep))
	mustMkdir(t, filepath.Join(right, deep))
	mustWrite(t, filepath.Join(left, deep, "leaf.txt"), "x")
	mustWrite(t, filepath.Join(right, deep, "leaf.txt"), "x")

	s := New(left, right, 2, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool {
		_, ok := s.Node("a/b/c/leaf.txt")
		return ok
	})
}

func TestRecursiveTriggerReachesLaterDiscoveredDescendants(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustMkdir(t, filepath.Join(left, "sub"))
	mustMkdir(t, filepath.Join(right, "sub"))
	mustWrite(t, filepath.Join(left, "sub", "f.txt"), "hello")
	mustWrite(t, filepath.Join(right, "sub", "f.txt"), "world")

	s := New(left, right, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed })

	// "sub" is very likely not listed yet at this point — sub/f.txt isn't
	// even a node yet — which is exactly the case this test exercises:
	// a recursive trigger must still reach it once listing catches up.
	s.TriggerCompare(s.Tree, diffmodel.Checksum, true)

	pump(t, s, 5*time.Second, func() bool {
		n, ok := s.Node("sub/f.txt")
		return ok && n.Level == diffmodel.Checksum
	})

	n, _ := s.Node("sub/f.txt")
	if n.Result != diffmodel.Differs {
		t.Fatalf("Result = %v; want Differs", n.Result)
	}
}

func TestRecursiveTriggerOnNotYetListedDirectoryStillArms(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustMkdir(t, filepath.Join(left, "sub"))
	mustMkdir(t, filepath.Join(right, "sub"))
	mustWrite(t, filepath.Join(left, "sub", "f.txt"), "hello")
	mustWrite(t, filepath.Join(right, "sub", "f.txt"), "world")

	s := New(left, right, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed })
	sub, ok := s.Node("sub")
	if !ok {
		t.Fatal("sub not found")
	}

	// Simulate navigating into sub and immediately triggering a recursive
	// compare before sub's own listing has completed: sub.Children is
	// still empty right now, since nothing has been pumped since root was
	// listed. The trigger must not be silently lost.
	s.Navigate(sub)
	s.TriggerCompare(sub, diffmodel.Checksum, true)

	pump(t, s, 5*time.Second, func() bool {
		n, ok := s.Node("sub/f.txt")
		return ok && n.Level == diffmodel.Checksum
	})
	n, _ := s.Node("sub/f.txt")
	if n.Result != diffmodel.Differs {
		t.Fatalf("Result = %v; want Differs", n.Result)
	}
}

// drainPending applies any results already sitting in the channels
// (non-blocking after a short allowance for trailing work to land), for
// tests asserting a settled state once no further work is expected.
func drainPending(t *testing.T, s *Session) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	for {
		select {
		case r := <-s.ListResults():
			s.OnListResult(r)
			continue
		case r := <-s.CompareResults():
			s.OnCompareResult(r)
			continue
		default:
		}
		break
	}
}

func TestNonRecursiveTriggerOnlyAffectsDirectChildren(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(left, "top.txt"), "same")
	mustWrite(t, filepath.Join(right, "top.txt"), "same")
	mustMkdir(t, filepath.Join(left, "sub"))
	mustMkdir(t, filepath.Join(right, "sub"))
	mustWrite(t, filepath.Join(left, "sub", "nested.txt"), "same")
	mustWrite(t, filepath.Join(right, "sub", "nested.txt"), "same")

	s := New(left, right, 2, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool {
		_, ok := s.Node("sub/nested.txt")
		return ok
	})

	s.TriggerCompare(s.Tree, diffmodel.Checksum, false)

	pump(t, s, 5*time.Second, func() bool {
		n, _ := s.Node("top.txt")
		return n.Level == diffmodel.Checksum
	})

	// Give any (incorrect) stray work a moment to land, then verify the
	// nested file was never touched.
	drainPending(t, s)

	nested, _ := s.Node("sub/nested.txt")
	if nested.Level != diffmodel.NotCompared {
		t.Fatalf("nested.Level = %v; want NotCompared (non-recursive trigger must not touch subdirectories)", nested.Level)
	}
}

func TestCancelPendingComparesDropsQueuedNotActive(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		mustWrite(t, filepath.Join(left, name), "x")
		mustWrite(t, filepath.Join(right, name), "x")
	}
	s := New(left, right, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed })

	s.TriggerCompare(s.Tree, diffmodel.Checksum, false)
	s.CancelPendingCompares()

	if stats := s.Stats(); stats.CmpPending != 0 {
		t.Fatalf("CmpPending = %d after cancel; want 0", stats.CmpPending)
	}
}

func TestSubtreePendingListingSettlesToZero(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	deep := filepath.Join("a", "b", "c")
	mustMkdir(t, filepath.Join(left, deep))
	mustMkdir(t, filepath.Join(right, deep))
	mustWrite(t, filepath.Join(left, deep, "leaf.txt"), "x")
	mustWrite(t, filepath.Join(right, deep, "leaf.txt"), "x")

	s := New(left, right, 2, diffmodel.NotCompared)
	defer s.Close()

	if s.Tree.PendingListingLeft == 0 || s.Tree.PendingListingRight == 0 {
		t.Fatalf("PendingListingLeft/Right = %d/%d right after New(); want both > 0, root's own listing was just enqueued for both sides", s.Tree.PendingListingLeft, s.Tree.PendingListingRight)
	}

	pump(t, s, 5*time.Second, func() bool {
		_, ok := s.Node("a/b/c/leaf.txt")
		return ok
	})
	drainPending(t, s)

	if s.Tree.PendingListingLeft != 0 || s.Tree.PendingListingRight != 0 {
		t.Fatalf("PendingListingLeft/Right = %d/%d once the whole tree is listed; want 0/0", s.Tree.PendingListingLeft, s.Tree.PendingListingRight)
	}
}

func TestPerSideListingPendingOnOneSidedDirectory(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustMkdir(t, filepath.Join(left, "onlyleft"))
	mustWrite(t, filepath.Join(left, "onlyleft", "f.txt"), "x")
	mustWrite(t, filepath.Join(left, "top.txt"), "x")
	mustWrite(t, filepath.Join(right, "top.txt"), "x")

	s := New(left, right, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed })

	onlyleft, ok := s.Node("onlyleft")
	if !ok {
		t.Fatal("onlyleft not found")
	}
	if onlyleft.Presence != diffmodel.LeftOnly {
		t.Fatalf("onlyleft.Presence = %v; want LeftOnly", onlyleft.Presence)
	}

	// onlyleft's own listing job was enqueued as soon as it was
	// discovered (inside the OnListResult call the pump above just
	// made), but its result hasn't been pumped yet, so it must still
	// register as pending — on the left only, since nothing exists to
	// list on the right.
	if onlyleft.PendingListingLeft == 0 {
		t.Fatal("PendingListingLeft = 0 for a still-listing left-only directory; want > 0")
	}
	if onlyleft.PendingListingRight != 0 {
		t.Fatalf("PendingListingRight = %d for a left-only directory; want 0, there's nothing to list on the right", onlyleft.PendingListingRight)
	}
	if s.Tree.PendingListingRight != 0 {
		t.Fatalf("root PendingListingRight = %d while only a left-only descendant is pending; want 0", s.Tree.PendingListingRight)
	}
	if s.Tree.PendingListingLeft == 0 {
		t.Fatal("root PendingListingLeft = 0 while onlyleft's listing is still pending; want > 0")
	}

	pump(t, s, 5*time.Second, func() bool { return onlyleft.Listed })
	drainPending(t, s)

	if onlyleft.PendingListingLeft != 0 || s.Tree.PendingListingLeft != 0 {
		t.Fatalf("PendingListingLeft nonzero once onlyleft has settled: onlyleft=%d root=%d", onlyleft.PendingListingLeft, s.Tree.PendingListingLeft)
	}
}

func TestSubtreePendingCompareTracksAncestorsAndClears(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustMkdir(t, filepath.Join(left, "sub"))
	mustMkdir(t, filepath.Join(right, "sub"))
	mustWrite(t, filepath.Join(left, "sub", "f.txt"), "hello")
	mustWrite(t, filepath.Join(right, "sub", "f.txt"), "world")

	s := New(left, right, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool {
		_, ok := s.Node("sub/f.txt")
		return ok
	})
	drainPending(t, s)

	sub, ok := s.Node("sub")
	if !ok {
		t.Fatal("sub not found")
	}
	if s.Tree.PendingCompare != 0 || sub.PendingCompare != 0 {
		t.Fatalf("PendingCompare nonzero before any compare triggered: root=%d sub=%d", s.Tree.PendingCompare, sub.PendingCompare)
	}

	s.TriggerCompare(s.Tree, diffmodel.Checksum, true)

	// armRecursive enqueues compare jobs for already-known descendants
	// synchronously, so the ancestor counts must already be raised here,
	// before anything is pumped.
	if s.Tree.PendingCompare == 0 || sub.PendingCompare == 0 {
		t.Fatalf("PendingCompare not raised on root/sub right after TriggerCompare: root=%d sub=%d", s.Tree.PendingCompare, sub.PendingCompare)
	}

	pump(t, s, 5*time.Second, func() bool {
		n, _ := s.Node("sub/f.txt")
		return n.Level == diffmodel.Checksum
	})
	drainPending(t, s)

	if s.Tree.PendingCompare != 0 || sub.PendingCompare != 0 {
		t.Fatalf("PendingCompare = root:%d sub:%d once the compare has completed; want 0/0", s.Tree.PendingCompare, sub.PendingCompare)
	}
}

func TestCancelPendingComparesClearsSubtreePendingCompare(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		mustWrite(t, filepath.Join(left, name), "x")
		mustWrite(t, filepath.Join(right, name), "x")
	}
	s := New(left, right, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed })

	s.TriggerCompare(s.Tree, diffmodel.Checksum, false)
	s.CancelPendingCompares()

	// A job already popped by the single worker before cancel ran is
	// still active and will still produce a result; wait for the queue
	// to fully settle before asserting the tree-side count follows suit.
	pump(t, s, 5*time.Second, func() bool {
		st := s.Stats()
		return st.CmpPending == 0 && st.CmpActive == 0
	})

	if s.Tree.PendingCompare != 0 {
		t.Fatalf("PendingCompare = %d after cancel and drain; want 0", s.Tree.PendingCompare)
	}
}

func TestAutoLevelFlagArmsWholeTree(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(left, "f.txt"), "aaa")
	mustWrite(t, filepath.Join(right, "f.txt"), "bbb")

	s := New(left, right, 2, diffmodel.SizeMtime)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool {
		n, ok := s.Node("f.txt")
		return ok && n.Level != diffmodel.NotCompared
	})
}
