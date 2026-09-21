package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/pairtree"
	"github.com/m42cel/dirdiff/internal/scan"
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

// counts records how much of each kind of work actually reached the
// tree, so a test can assert not just what a level concluded but what it
// had to read to conclude it.
type counts struct{ lists, stats, compares int }

// pump simulates the UI's Update loop: it's the only goroutine allowed to
// mutate the tree, draining result channels into it until cond is true.
func (c *counts) pump(t *testing.T, s *Session, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if cond() {
			return
		}
		select {
		case r := <-s.ListResults():
			c.lists++
			s.OnListResult(r)
		case r := <-s.StatResults():
			c.stats++
			s.OnStatResult(r)
		case r := <-s.CompareResults():
			c.compares++
			s.OnCompareResult(r)
		case <-deadline:
			t.Fatal("timed out waiting for condition")
		}
	}
}

func pump(t *testing.T, s *Session, timeout time.Duration, cond func() bool) {
	t.Helper()
	new(counts).pump(t, s, timeout, cond)
}

func TestBFSEventuallyListsWholeTree(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	deep := filepath.Join("a", "b", "c")
	mustMkdir(t, filepath.Join(left, deep))
	mustMkdir(t, filepath.Join(right, deep))
	mustWrite(t, filepath.Join(left, deep, "leaf.txt"), "x")
	mustWrite(t, filepath.Join(right, deep, "leaf.txt"), "x")

	s := New(left, right, 2, 2, diffmodel.NotCompared)
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

	s := New(left, right, 1, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed() })

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

	s := New(left, right, 1, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed() })
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

// drain applies any results already sitting in the channels
// (non-blocking after a short allowance for trailing work to land), for
// tests asserting a settled state once no further work is expected.
func (c *counts) drain(t *testing.T, s *Session) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	for {
		select {
		case r := <-s.ListResults():
			c.lists++
			s.OnListResult(r)
			continue
		case r := <-s.StatResults():
			c.stats++
			s.OnStatResult(r)
			continue
		case r := <-s.CompareResults():
			c.compares++
			s.OnCompareResult(r)
			continue
		default:
		}
		break
	}
}

func drainPending(t *testing.T, s *Session) {
	t.Helper()
	new(counts).drain(t, s)
}

func TestNonRecursiveTriggerOnlyAffectsDirectChildren(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(left, "top.txt"), "same")
	mustWrite(t, filepath.Join(right, "top.txt"), "same")
	mustMkdir(t, filepath.Join(left, "sub"))
	mustMkdir(t, filepath.Join(right, "sub"))
	mustWrite(t, filepath.Join(left, "sub", "nested.txt"), "same")
	mustWrite(t, filepath.Join(right, "sub", "nested.txt"), "same")

	s := New(left, right, 2, 2, diffmodel.NotCompared)
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
	s := New(left, right, 1, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed() })

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

	s := New(left, right, 2, 2, diffmodel.NotCompared)
	defer s.Close()

	if l, r := s.Tree.PendingListing(); l == 0 || r == 0 {
		t.Fatalf("PendingListing = %d/%d right after New(); want both > 0, each root's own listing was just enqueued", l, r)
	}

	pump(t, s, 5*time.Second, func() bool {
		_, ok := s.Node("a/b/c/leaf.txt")
		return ok
	})
	drainPending(t, s)

	if l, r := s.Tree.PendingListing(); l != 0 || r != 0 {
		t.Fatalf("PendingListing = %d/%d once the whole tree is listed; want 0/0", l, r)
	}
}

func TestPerSideListingPendingOnOneSidedDirectory(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustMkdir(t, filepath.Join(left, "onlyleft"))
	mustWrite(t, filepath.Join(left, "onlyleft", "f.txt"), "x")
	mustWrite(t, filepath.Join(left, "top.txt"), "x")
	mustWrite(t, filepath.Join(right, "top.txt"), "x")

	s := New(left, right, 1, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed() })

	onlyleft, ok := s.Node("onlyleft")
	if !ok {
		t.Fatal("onlyleft not found")
	}
	if onlyleft.Presence() != diffmodel.LeftOnly {
		t.Fatalf("onlyleft.Presence = %v; want LeftOnly", onlyleft.Presence())
	}

	// onlyleft's own listing job was enqueued as soon as the left side
	// discovered it (inside the OnListResult call the pump above just
	// made), but its result hasn't been pumped yet, so it must still
	// register as pending — on the left only, since nothing exists to
	// list on the right.
	pendingLeft, pendingRight := onlyleft.PendingListing()
	if pendingLeft == 0 {
		t.Fatal("left PendingListing = 0 for a still-listing left-only directory; want > 0")
	}
	if pendingRight != 0 {
		t.Fatalf("right PendingListing = %d for a left-only directory; want 0, there's nothing to list on the right", pendingRight)
	}
	rootLeft, rootRight := s.Tree.PendingListing()
	if rootRight != 0 {
		t.Fatalf("root right PendingListing = %d while only a left-only descendant is pending; want 0", rootRight)
	}
	if rootLeft == 0 {
		t.Fatal("root left PendingListing = 0 while onlyleft's listing is still pending; want > 0")
	}

	pump(t, s, 5*time.Second, func() bool { return onlyleft.Listed() })
	drainPending(t, s)

	pendingLeft, _ = onlyleft.PendingListing()
	rootLeft, _ = s.Tree.PendingListing()
	if pendingLeft != 0 || rootLeft != 0 {
		t.Fatalf("left PendingListing nonzero once onlyleft has settled: onlyleft=%d root=%d", pendingLeft, rootLeft)
	}
}

func TestSubtreePendingCompareTracksAncestorsAndClears(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustMkdir(t, filepath.Join(left, "sub"))
	mustMkdir(t, filepath.Join(right, "sub"))
	mustWrite(t, filepath.Join(left, "sub", "f.txt"), "hello")
	mustWrite(t, filepath.Join(right, "sub", "f.txt"), "world")

	s := New(left, right, 1, 1, diffmodel.NotCompared)
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
	if s.Tree.ExaminePending() || sub.ExaminePending() {
		t.Fatal("examination already pending before any compare was triggered")
	}

	s.TriggerCompare(s.Tree, diffmodel.Checksum, true)

	// armRecursive enqueues jobs for already-known descendants
	// synchronously, so the ancestor counts must already be raised here,
	// before anything is pumped. A content trigger starts with the two
	// sides' metadata, so what's outstanding right now is the stat pair.
	if !s.Tree.ExaminePending() || !sub.ExaminePending() {
		t.Fatal("no examination pending on root/sub right after TriggerCompare")
	}

	pump(t, s, 5*time.Second, func() bool {
		n, _ := s.Node("sub/f.txt")
		return n.Level == diffmodel.Checksum
	})
	drainPending(t, s)

	if s.Tree.ExaminePending() || sub.ExaminePending() {
		t.Fatalf("examination still pending once the compare has completed: root compare=%d sub compare=%d", s.Tree.PendingCompare, sub.PendingCompare)
	}
}

func TestCancelPendingComparesClearsSubtreePendingCompare(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		mustWrite(t, filepath.Join(left, name), "x")
		mustWrite(t, filepath.Join(right, name), "x")
	}
	s := New(left, right, 1, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed() })

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

// TestFileDirNameCollisionListingSettles covers SPEC.md §3.1: a file and
// a directory sharing a name are matched independently by (name, type)
// into two unrelated rows at the same path. The directory row must still
// get its own listing result routed to it — which it now does by being a
// node of the left side's own tree, where a name is unambiguous, rather
// than via a path index shared with the file it collides with.
func TestFileDirNameCollisionListingSettles(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustMkdir(t, filepath.Join(left, "clash"))
	mustWrite(t, filepath.Join(left, "clash", "nested.txt"), "x")
	mustWrite(t, filepath.Join(right, "clash"), "a file, not a directory")

	s := New(left, right, 2, 2, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed() })

	var dirNode, fileNode *pairtree.Node
	for _, c := range s.Tree.Children {
		if c.Name() != "clash" {
			continue
		}
		if c.IsDir() {
			dirNode = c
		} else {
			fileNode = c
		}
	}
	if dirNode == nil || fileNode == nil {
		t.Fatalf("expected both a dir and a file named clash as children of root; got dir=%v file=%v", dirNode, fileNode)
	}
	if dirNode.Presence() != diffmodel.LeftOnly {
		t.Fatalf("dirNode.Presence = %v; want LeftOnly", dirNode.Presence())
	}
	if fileNode.Presence() != diffmodel.RightOnly {
		t.Fatalf("fileNode.Presence = %v; want RightOnly", fileNode.Presence())
	}

	pump(t, s, 5*time.Second, func() bool { return dirNode.Listed() })
	drainPending(t, s)

	if dirNode.Left.Listing {
		t.Fatal("dirNode is still marked listing after its listing result should have been applied")
	}
	if len(dirNode.Children) != 1 || dirNode.Children[0].Name() != "nested.txt" {
		t.Fatalf("dirNode.Children = %v; want [nested.txt]", dirNode.Children)
	}
	if pendingLeft, _ := dirNode.PendingListing(); pendingLeft != 0 {
		t.Fatalf("dirNode left PendingListing = %d after settling; want 0", pendingLeft)
	}
	if rootLeft, _ := s.Tree.PendingListing(); rootLeft != 0 {
		t.Fatalf("root left PendingListing = %d after settling; want 0", rootLeft)
	}
}

// The metadata level costs no content reads at all: every verdict it
// produces is an equality test over two stat results.
func TestMetadataLevelOpensNoFiles(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(left, "same.txt"), "hello")
	mustWrite(t, filepath.Join(right, "same.txt"), "hello")
	mustWrite(t, filepath.Join(left, "diff.txt"), "hello")
	mustWrite(t, filepath.Join(right, "diff.txt"), "hello!!!")
	if err := os.Symlink("/a", filepath.Join(left, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/b", filepath.Join(right, "link")); err != nil {
		t.Fatal(err)
	}
	sameTime := time.Now().Add(-time.Hour)
	for _, dir := range []string{left, right} {
		if err := os.Chtimes(filepath.Join(dir, "same.txt"), sameTime, sameTime); err != nil {
			t.Fatal(err)
		}
	}

	s := New(left, right, 2, 2, diffmodel.SizeMtime)
	defer s.Close()

	var c counts
	c.pump(t, s, 5*time.Second, func() bool {
		for _, name := range []string{"same.txt", "diff.txt", "link"} {
			n, ok := s.Node(name)
			if !ok || n.Level == diffmodel.NotCompared {
				return false
			}
		}
		return true
	})
	c.drain(t, s)

	if c.compares != 0 {
		t.Errorf("%d content comparisons ran at --level=metadata; want none", c.compares)
	}
	for name, want := range map[string]diffmodel.CompareResult{
		"same.txt": diffmodel.Same,
		"diff.txt": diffmodel.Differs,
		"link":     diffmodel.Differs,
	} {
		if n, _ := s.Node(name); n.Result != want {
			t.Errorf("%s: Result = %v; want %v", name, n.Result, want)
		}
	}
}

// Two files of different lengths can't have equal content, so the
// content level settles them from their sizes and never creates the job
// (SPEC.md §5.1) — the one real read is the same-size pair, where the
// sizes say nothing.
func TestContentLevelSkipsFilesWhoseSizesAlreadyDiffer(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(left, "sizes-differ.txt"), "hello")
	mustWrite(t, filepath.Join(right, "sizes-differ.txt"), "hello!!!")
	mustWrite(t, filepath.Join(left, "same-size.txt"), "aaaa")
	mustWrite(t, filepath.Join(right, "same-size.txt"), "bbbb")

	s := New(left, right, 2, 2, diffmodel.Checksum)
	defer s.Close()

	var c counts
	c.pump(t, s, 5*time.Second, func() bool {
		for _, name := range []string{"sizes-differ.txt", "same-size.txt"} {
			n, ok := s.Node(name)
			if !ok || n.Level != diffmodel.Checksum {
				return false
			}
		}
		return true
	})
	c.drain(t, s)

	if c.compares != 1 {
		t.Errorf("%d content comparisons ran; want exactly 1 — only the same-size pair needs reading", c.compares)
	}
	for _, name := range []string{"sizes-differ.txt", "same-size.txt"} {
		n, _ := s.Node(name)
		if n.Result != diffmodel.Differs || n.Level != diffmodel.Checksum {
			t.Errorf("%s: Result=%v Level=%v; want Differs at the content level either way", name, n.Result, n.Level)
		}
	}
}

// The gap this closes: a one-sided entry has no counterpart to be
// compared against, so it was never measured and its subtree reported
// "size ?" forever (SPEC.md §4.2).
func TestOneSidedEntriesAreMeasured(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustMkdir(t, filepath.Join(left, "onlyleft"))
	mustWrite(t, filepath.Join(left, "onlyleft", "inside.txt"), "0123456789")

	s := New(left, right, 2, 2, diffmodel.SizeMtime)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool {
		n, ok := s.Node("onlyleft/inside.txt")
		return ok && n.Left.HaveStat
	})

	onlyleft, _ := s.Node("onlyleft")
	totals, _ := onlyleft.SideTotals()
	if totals.Size != 10 || totals.SizedFiles != 1 || totals.Files != 1 {
		t.Fatalf("left-only subtree totals = %+v; want its one file fully sized at 10 bytes", totals)
	}
}

// Monotonicity (SPEC.md §5.3) across the two result kinds: metadata
// arriving after a content verdict updates the size the details panel
// shows without touching the verdict.
func TestStatResultAfterAContentVerdictKeepsTheVerdict(t *testing.T) {
	s := New(t.TempDir(), t.TempDir(), 1, 1, diffmodel.NotCompared)
	defer s.Close()

	for _, sd := range diffmodel.Sides {
		s.OnListResult(scan.ListResult{Side: sd, RelPath: "", Entries: []diffmodel.ListedEntry{
			{Name: "f.txt", Type: diffmodel.File},
		}})
	}
	n, ok := s.Node("f.txt")
	if !ok {
		t.Fatal("f.txt not found")
	}

	s.OnCompareResult(scan.CompareOutcome{RelPath: "f.txt", Level: diffmodel.Checksum, Result: diffmodel.Same})
	for _, sd := range diffmodel.Sides {
		s.OnStatResult(scan.StatResult{Side: sd, RelPath: "f.txt", Size: 2048, Mtime: time.Unix(int64(sd), 0)})
	}

	if n.Level != diffmodel.Checksum || n.Result != diffmodel.Same {
		t.Fatalf("Level=%v Result=%v; want the content verdict kept despite the differing mtimes", n.Level, n.Result)
	}
	if left, _ := s.Tree.SideTotals(); left.Size != 2048 || left.SizedFiles != 1 {
		t.Fatalf("root left totals = %d bytes over %d files; want 2048 over 1", left.Size, left.SizedFiles)
	}
}

// Metadata reads are triggered work like any other, so the cancel key
// drops them too — and unwinds the per-side counts the dropped jobs were
// being tracked by (SPEC.md §5.4).
func TestCancelPendingComparesDropsQueuedStatJobs(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		mustWrite(t, filepath.Join(left, name), "x")
		mustWrite(t, filepath.Join(right, name), "x")
	}
	s := New(left, right, 1, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed() })

	s.TriggerCompare(s.Tree, diffmodel.SizeMtime, false)
	if l, _ := s.Tree.PendingStat(); l == 0 {
		t.Fatal("left PendingStat = 0 right after a metadata trigger; want the stat jobs counted")
	}
	s.CancelPendingCompares()

	if stats := s.Stats(); stats.CmpPending != 0 {
		t.Fatalf("CmpPending = %d after cancel; want 0", stats.CmpPending)
	}
	// Whatever the single worker had already picked up still reports back.
	pump(t, s, 5*time.Second, func() bool {
		st := s.Stats()
		return st.CmpPending == 0 && st.CmpActive == 0
	})
	drainPending(t, s)

	if l, r := s.Tree.PendingStat(); l != 0 || r != 0 {
		t.Fatalf("PendingStat = %d/%d after cancel and drain; want 0/0", l, r)
	}
}

func TestSetListWorkersGrowsAndShrinksWithoutLosingWork(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("d%d", i)
		mustMkdir(t, filepath.Join(left, name))
		mustMkdir(t, filepath.Join(right, name))
		mustWrite(t, filepath.Join(left, name, "f.txt"), "x")
		mustWrite(t, filepath.Join(right, name, "f.txt"), "x")
	}

	s := New(left, right, 1, 1, diffmodel.NotCompared)
	defer s.Close()

	if got := s.ListWorkers(); got != 1 {
		t.Fatalf("ListWorkers() = %d right after New(1, ...); want 1", got)
	}

	s.SetListWorkers(4)
	if got := s.ListWorkers(); got != 4 {
		t.Fatalf("ListWorkers() = %d after SetListWorkers(4); want 4", got)
	}

	// Shrink back down mid-flight — none of the 20 subdirectories'
	// listings (already queued from New()'s root listing once pumped)
	// should be lost, whether they're picked up by a worker that then
	// exits, or one that survives the shrink.
	s.SetListWorkers(1)
	if got := s.ListWorkers(); got != 1 {
		t.Fatalf("ListWorkers() = %d after SetListWorkers(1); want 1 (target changes immediately, independent of how many goroutines have actually exited yet)", got)
	}

	pump(t, s, 5*time.Second, func() bool {
		for i := 0; i < 20; i++ {
			if _, ok := s.Node(fmt.Sprintf("d%d/f.txt", i)); !ok {
				return false
			}
		}
		return true
	})
}

func TestSetCompareWorkersGrowsAndShrinksWithoutLosingWork(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		mustWrite(t, filepath.Join(left, name), "x")
		mustWrite(t, filepath.Join(right, name), "y")
	}

	s := New(left, right, 1, 1, diffmodel.NotCompared)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool { return s.Tree.Listed() })

	s.SetCompareWorkers(4)
	s.TriggerCompare(s.Tree, diffmodel.Checksum, false)
	s.SetCompareWorkers(1)
	if got := s.CompareWorkers(); got != 1 {
		t.Fatalf("CompareWorkers() = %d after SetCompareWorkers(1); want 1", got)
	}

	pump(t, s, 5*time.Second, func() bool {
		for i := 0; i < 20; i++ {
			n, ok := s.Node(fmt.Sprintf("f%d.txt", i))
			if !ok || n.Level != diffmodel.Checksum {
				return false
			}
		}
		return true
	})
}

func TestAutoLevelFlagArmsWholeTree(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(left, "f.txt"), "aaa")
	mustWrite(t, filepath.Join(right, "f.txt"), "bbb")

	s := New(left, right, 2, 2, diffmodel.SizeMtime)
	defer s.Close()

	pump(t, s, 5*time.Second, func() bool {
		n, ok := s.Node("f.txt")
		return ok && n.Level != diffmodel.NotCompared
	})
}
