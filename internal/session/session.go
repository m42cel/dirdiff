// Package session orchestrates dirdiff's background work: it owns the
// tree, the two worker pools (listing and comparison, SPEC.md §8.2), and
// the logic that decides what to enqueue and at what priority. It is
// deliberately independent of Bubble Tea — it exposes plain channels for
// results, which package ui wraps into tea.Cmd/tea.Msg. All tree
// mutation happens on the caller's goroutine (the UI's Update loop);
// Session itself only touches the concurrency-safe worker queues.
package session

import (
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

	nodeIndex map[string]*tree.Node

	listQ *workqueue.Queue[scan.ListJob]
	cmpQ  *workqueue.Queue[scan.CompareJob]

	listResults chan scan.ListResult
	cmpResults  chan scan.CompareOutcome
}

// New creates a Session, starts its worker pools, and enqueues the
// initial listing of the root directory at High priority so the first
// level is shown as soon as possible (SPEC.md §8.1). If autoLevel is not
// NotCompared, the whole tree is armed to auto-compare recursively at that
// level in the background at Low priority as listing discovers it
// (SPEC.md §2.1's --compare-level flag, size-date by default).
func New(leftRoot, rightRoot string, workers int, autoLevel diffmodel.CompareLevel) *Session {
	root := tree.NewRoot()
	s := &Session{
		LeftRoot: leftRoot, RightRoot: rightRoot,
		Tree:      root,
		nodeIndex: map[string]*tree.Node{"": root},
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

	if workers < 1 {
		workers = 1
	}
	s.runListWorkers(workers)
	s.runCompareWorkers(workers)

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

func (s *Session) runListWorkers(n int) {
	for i := 0; i < n; i++ {
		go func() {
			for {
				job, key, ok := s.listQ.Pop()
				if !ok {
					return
				}
				result := scan.DoList(job)
				s.listQ.Done(key)
				s.listResults <- result
			}
		}()
	}
}

func (s *Session) runCompareWorkers(n int) {
	for i := 0; i < n; i++ {
		go func() {
			for {
				job, key, ok := s.cmpQ.Pop()
				if !ok {
					return
				}
				outcome := scan.DoCompare(job)
				s.cmpQ.Done(key)
				s.cmpResults <- outcome
			}
		}()
	}
}

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

// Node looks up a node by RelPath, if it's been discovered yet.
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
	n, ok := s.nodeIndex[r.RelPath]
	if !ok {
		return
	}
	tree.ApplyListing(n, r.Children, r.LeftErr, r.RightErr)
	leftDelta, rightDelta := pendingListingDeltas(n.Presence, -1)
	tree.AdjustPendingListing(n, leftDelta, rightDelta)

	for _, c := range n.Children {
		s.nodeIndex[c.RelPath] = c
		if c.IsDir() {
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
