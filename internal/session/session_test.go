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
