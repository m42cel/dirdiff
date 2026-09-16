// Package session orchestrates dirdiff's background work: it owns the
// tree, the two worker pools (listing and comparison, SPEC.md §8.2), and
// the logic that decides what to enqueue and at what priority. It is
// deliberately independent of Bubble Tea — it exposes plain channels for
// results, which package ui wraps into tea.Cmd/tea.Msg. All tree
// mutation happens on the caller's goroutine (the UI's Update loop);
// Session itself only touches the concurrency-safe worker queues.
package session

import (
	"sync/atomic"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/scan"
	"github.com/m42cel/dirdiff/internal/tree"
	"github.com/m42cel/dirdiff/internal/workqueue"
)

// Session holds everything needed to scan and compare two directory
// trees in the background.
type Session struct {
	LeftRoot, RightRoot string
	Tree                *tree.Node

	// nodeIndex is keyed by RelPath alone, so it can't distinguish a file
	// from a directory of the same name (SPEC.md §3.1 matches those as
	// two unrelated rows sharing one RelPath) — fine for OnCompareResult,
	// since a compare job only ever targets a both-sided entry and two
	// entries can only share a RelPath by having different, one-sided
	// presences (see dirIndex below). Not safe for directory listing
	// lookups, which is what dirIndex is for.
	nodeIndex map[string]*tree.Node

	// dirIndex mirrors nodeIndex but holds only directory nodes, so
	// OnListResult can resolve a listing result to the right node even
	// when a same-named file collides with it in nodeIndex (last one
	// indexed there wins, and children are indexed dirs-first — see
	// sortChildren — so a colliding file always wins nodeIndex, which
	// would otherwise misroute the directory's own listing result onto
	// the file node and leave the directory's Listing flag stuck true).
	dirIndex map[string]*tree.Node

	listQ *workqueue.Queue[scan.ListJob]
	cmpQ  *workqueue.Queue[scan.CompareJob]

	listResults chan scan.ListResult
	cmpResults  chan scan.CompareOutcome

	// listTarget/cmpTarget are each pool's configured size (SPEC.md §8.2's
	// --scan-workers/--compare-workers, adjustable at runtime); listLive/
	// cmpLive count the goroutines actually running right now. Set*Workers
	// only ever adds goroutines to grow a pool — shrinking asks the excess
	// to exit itself (see shrinkIfExcess) rather than interrupting
	// in-flight work, so live can briefly exceed or trail target.
	listTarget, listLive atomic.Int32
	cmpTarget, cmpLive   atomic.Int32
}

// New creates a Session, starts its worker pools, and enqueues the
// initial listing of the root directory at High priority so the first
// level is shown as soon as possible (SPEC.md §8.1). If autoLevel is not
// NotCompared, the whole tree is armed to auto-compare recursively at that
// level in the background at Low priority as listing discovers it
// (SPEC.md §2.1's --compare-level flag, size-date by default).
//
// listWorkers and compareWorkers size the two pools independently
// (SPEC.md §8.2's --scan-workers/--compare-workers): listing is cheap,
// low-CPU directory-metadata I/O that doesn't benefit from scaling with
// core count (and on a mechanical disk, more concurrent listing jobs can
// mean more seeking for no throughput gain), while comparison — especially
// at the content level — does real per-byte CPU work alongside the I/O,
// so scaling it with GOMAXPROCS is the more defensible default of the two.
func New(leftRoot, rightRoot string, listWorkers, compareWorkers int, autoLevel diffmodel.CompareLevel) *Session {
	root := tree.NewRoot()
	s := &Session{
		LeftRoot: leftRoot, RightRoot: rightRoot,
		Tree:      root,
		nodeIndex: map[string]*tree.Node{"": root},
		dirIndex:  map[string]*tree.Node{"": root},
		listQ:     workqueue.New[scan.ListJob](),
		cmpQ:      workqueue.New[scan.CompareJob](),
		// Buffered so workers never block handing off a result while the
		// UI is busy processing the previous one.
		listResults: make(chan scan.ListResult, 64),
		cmpResults:  make(chan scan.CompareOutcome, 64),
	}

	if autoLevel != diffmodel.NotCompared {
		root.PendingRecursiveLevel = autoLevel
	}

	s.SetListWorkers(listWorkers)
	s.SetCompareWorkers(compareWorkers)

	s.enqueueList(root)

	return s
}

// ListResults and CompareResults are consumed by package ui to build
// long-lived listening tea.Cmds.
func (s *Session) ListResults() <-chan scan.ListResult        { return s.listResults }
func (s *Session) CompareResults() <-chan scan.CompareOutcome { return s.cmpResults }

// Close shuts down both worker pools. Safe to call once, e.g. after the
// Bubble Tea program exits.
func (s *Session) Close() {
	s.listQ.Close()
	s.cmpQ.Close()
}

func (s *Session) listWorkerLoop() {
	for {
		job, key, ok := s.listQ.PopUnless(func() bool { return shrinkIfExcess(&s.listLive, &s.listTarget) })
		if !ok {
			return
		}
		result := scan.DoList(job)
		s.listQ.Done(key)
		s.listResults <- result
	}
}

func (s *Session) compareWorkerLoop() {
	for {
		job, key, ok := s.cmpQ.PopUnless(func() bool { return shrinkIfExcess(&s.cmpLive, &s.cmpTarget) })
		if !ok {
			return
		}
		outcome := scan.DoCompare(job)
		s.cmpQ.Done(key)
		s.cmpResults <- outcome
	}
}

// shrinkIfExcess atomically decrements live and reports true if live
// currently exceeds target — a worker's own exit condition when its pool
// is resized down. The load-compare-CAS loop means that if several
// workers race this at once (e.g. right after a Wake()), exactly
// live-target of them see true and exit, never more — a shrink can't
// overshoot its new target regardless of how many idle workers wake
// simultaneously.
func shrinkIfExcess(live, target *atomic.Int32) bool {
	for {
		l, t := live.Load(), target.Load()
		if l <= t {
			return false
		}
		if live.CompareAndSwap(l, l-1) {
			return true
		}
	}
}

// SetListWorkers resizes the listing pool to n (clamped to at least 1;
// SPEC.md §8.2/§4.8's --scan-workers and its runtime 'w' popup
// equivalent). Growing spawns the additional workers immediately;
// shrinking never interrupts a job already in flight — the same "let it
// finish" policy as CancelPendingCompares (SPEC.md §5.4) — it just asks
// the excess to exit at its own next opportunity, promptly if currently
// idle (via Queue.Wake) or otherwise once its current job completes.
func (s *Session) SetListWorkers(n int) {
	if n < 1 {
		n = 1
	}
	old := s.listTarget.Swap(int32(n))
	switch {
	case int32(n) > old:
		for i := old; i < int32(n); i++ {
			s.listLive.Add(1)
			go s.listWorkerLoop()
		}
	case int32(n) < old:
		s.listQ.Wake()
	}
}

// SetCompareWorkers is SetListWorkers for the comparison pool.
func (s *Session) SetCompareWorkers(n int) {
	if n < 1 {
		n = 1
	}
	old := s.cmpTarget.Swap(int32(n))
	switch {
	case int32(n) > old:
		for i := old; i < int32(n); i++ {
			s.cmpLive.Add(1)
			go s.compareWorkerLoop()
		}
	case int32(n) < old:
		s.cmpQ.Wake()
	}
}

// ListWorkers and CompareWorkers report each pool's currently configured
// size (not how many of its goroutines happen to be live at this exact
// instant — see listTarget's doc comment), for the UI's 'w' popup to
// display and prefill for editing.
func (s *Session) ListWorkers() int    { return int(s.listTarget.Load()) }
func (s *Session) CompareWorkers() int { return int(s.cmpTarget.Load()) }

func (s *Session) enqueueList(n *tree.Node) {
	n.Listing = true
	created := s.listQ.Upsert(n.RelPath, scan.ListJob{
		RelPath:  n.RelPath,
		LeftAbs:  scan.AbsPath(s.LeftRoot, n.RelPath),
		RightAbs: scan.AbsPath(s.RightRoot, n.RelPath),
	}, nil)
	if created {
		leftDelta, rightDelta := pendingListingDeltas(n.Presence, 1)
		tree.AdjustPendingListing(n, leftDelta, rightDelta)
	}
}

// pendingListingDeltas reports which side(s) a listing job for a node
// with the given presence actually does work on — a one-sided node's
// listing job only ever reads the side it exists on, so only that side
// should register as pending.
func pendingListingDeltas(presence diffmodel.Presence, delta int) (left, right int) {
	if presence != diffmodel.RightOnly {
		left = delta
	}
	if presence != diffmodel.LeftOnly {
		right = delta
	}
	return left, right
}

// Node looks up a node by RelPath, if it's been discovered yet. If a
// file and directory of the same name collide at relPath (SPEC.md §3.1),
// this returns whichever was indexed last — use dirIndex-backed lookups
// (as OnListResult does) when the directory specifically is required.
func (s *Session) Node(relPath string) (*tree.Node, bool) {
	n, ok := s.nodeIndex[relPath]
	return n, ok
}

// OnListResult applies a completed listing to the tree, indexes the new
// children, continues the ambient breadth-first scan into any newly
// discovered subdirectories (SPEC.md §8.1), and — if this directory was
// armed for a recursive compare that started before it was listed —
// applies that comparison to the newly discovered children now (SPEC.md
// §5.2/§5.4).
func (s *Session) OnListResult(r scan.ListResult) {
	n, ok := s.dirIndex[r.RelPath]
	if !ok {
		return
	}
	tree.ApplyListing(n, r.Children, r.LeftErr, r.RightErr)
	leftDelta, rightDelta := pendingListingDeltas(n.Presence, -1)
	tree.AdjustPendingListing(n, leftDelta, rightDelta)

	for _, c := range n.Children {
		s.nodeIndex[c.RelPath] = c
		if c.IsDir() {
			s.dirIndex[c.RelPath] = c
			s.enqueueList(c)
		}
	}

	if n.PendingRecursiveLevel != diffmodel.NotCompared {
		s.armRecursive(n.Children, n.PendingRecursiveLevel)
	}
}

// OnCompareResult applies a completed comparison to the tree.
func (s *Session) OnCompareResult(r scan.CompareOutcome) {
	n, ok := s.nodeIndex[r.RelPath]
	if !ok {
		return
	}
	tree.ApplyCompareResult(n, r.Level, r.Result, r.Err, r.Stat)
	tree.AdjustPendingCompare(n, -1)
}

// Navigate reprioritizes both worker queues for a directory change: to
// becomes the new focus path (SPEC.md §8.3), so every still-queued job
// anywhere in to's subtree — at any depth, not just its direct children —
// now pops ahead of everything outside it, ordered by tree-edge distance
// from to. Moving the cursor within an already-listed directory should
// not call this — only changing the current directory does.
func (s *Session) Navigate(to *tree.Node) {
	if to == nil {
		return
	}
	if !to.Listed && !to.Listing {
		s.enqueueList(to)
	}
	s.listQ.SetFocus(to.RelPath)
	s.cmpQ.SetFocus(to.RelPath)
}

// TriggerCompare starts a comparison at level for dir's children
// (SPEC.md §5.2). If recursive is false, only dir's direct file/symlink
// children are compared. If recursive is true, the entire subtree rooted
// at dir is armed: already-known descendants are enqueued immediately,
// and any not yet discovered by the background listing scan are picked
// up as they're found (via OnListResult). Either way, dir is normally
// also the current navigation focus, so these jobs already sort ahead of
// unrelated background work (SPEC.md §8.3) without needing a priority of
// their own.
func (s *Session) TriggerCompare(dir *tree.Node, level diffmodel.CompareLevel, recursive bool) {
	if dir == nil || !dir.IsDir() {
		return
	}
	if recursive {
		// Arm dir itself, not just its current children: if dir hasn't
		// finished listing yet, dir.Children is still empty right now, so
		// without this the trigger would be silently lost instead of
		// reaching children discovered once listing completes.
		if level > dir.PendingRecursiveLevel {
			dir.PendingRecursiveLevel = level
		}
		s.armRecursive(dir.Children, level)
		return
	}
	for _, c := range dir.Children {
		if !c.IsDir() {
			s.maybeEnqueueCompare(c, level)
		}
	}
}

// armRecursive marks each directory in nodes (and, transitively, every
// already-listed descendant directory) as pending level, and enqueues
// compare jobs for every currently known file/symlink descendant.
func (s *Session) armRecursive(nodes []*tree.Node, level diffmodel.CompareLevel) {
	for _, c := range nodes {
		if c.Presence != diffmodel.Both {
			continue // nothing to compare against on the missing side
		}
		if c.IsDir() {
			if level > c.PendingRecursiveLevel {
				c.PendingRecursiveLevel = level
			}
			if c.Listed {
				s.armRecursive(c.Children, level)
			}
			continue
		}
		s.maybeEnqueueCompare(c, level)
	}
}

func (s *Session) maybeEnqueueCompare(n *tree.Node, level diffmodel.CompareLevel) {
	if n.Presence != diffmodel.Both {
		return
	}
	if n.Level >= level {
		return // already have an equal-or-deeper result (SPEC.md §5.3)
	}
	job := scan.CompareJob{
		RelPath: n.RelPath,
		LeftAbs: scan.AbsPath(s.LeftRoot, n.RelPath), RightAbs: scan.AbsPath(s.RightRoot, n.RelPath),
		Type: n.Type, Level: level,
	}
	created := s.cmpQ.Upsert(n.RelPath, job, func(old scan.CompareJob) scan.CompareJob {
		if level > old.Level {
			old.Level = level
		}
		return old
	})
	if created {
		tree.AdjustPendingCompare(n, 1)
	}
}

// CancelPendingCompares drops every not-yet-started queued comparison
// job (the global cancel key, SPEC.md §5.4). In-flight jobs already
// picked up by a worker finish normally. Dropped jobs will now never
// produce a result, so their subtree pending counts are unwound here
// instead of via OnCompareResult.
func (s *Session) CancelPendingCompares() {
	for _, key := range s.cmpQ.Clear() {
		if n, ok := s.nodeIndex[key]; ok {
			tree.AdjustPendingCompare(n, -1)
		}
	}
}

// QueueStats backs the status bar (SPEC.md §4.4).
type QueueStats struct {
	ListPending, ListActive int
	CmpPending, CmpActive   int
}

func (s *Session) Stats() QueueStats {
	return QueueStats{
		ListPending: s.listQ.PendingCount(), ListActive: s.listQ.ActiveCount(),
		CmpPending: s.cmpQ.PendingCount(), CmpActive: s.cmpQ.ActiveCount(),
	}
}

// IsListPending reports whether relPath's directory listing is queued
// or in-flight.
func (s *Session) IsListPending(relPath string) bool { return s.listQ.IsPending(relPath) }

// IsComparePending reports whether relPath has a queued or in-flight
// comparison job.
func (s *Session) IsComparePending(relPath string) bool { return s.cmpQ.IsPending(relPath) }
