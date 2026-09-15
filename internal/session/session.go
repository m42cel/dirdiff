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
// NotCompared, the whole tree is armed to auto-compare at that level in
// the background at Low priority as listing discovers it (SPEC.md
// §2.1's --compare-level flag).
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
		root.PendingRecursivePriority = workqueue.Low
	}

	if workers < 1 {
		workers = 1
	}
	s.runListWorkers(workers)
	s.runCompareWorkers(workers)

	s.enqueueList(root, workqueue.High)

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

func (s *Session) enqueueList(n *tree.Node, prio workqueue.Priority) {
	n.Listing = true
	s.listQ.Upsert(n.RelPath, prio, scan.ListJob{
		RelPath:  n.RelPath,
		LeftAbs:  scan.AbsPath(s.LeftRoot, n.RelPath),
		RightAbs: scan.AbsPath(s.RightRoot, n.RelPath),
	}, nil)
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

	for _, c := range n.Children {
		s.nodeIndex[c.RelPath] = c
		if c.IsDir() {
			s.enqueueList(c, workqueue.Low)
		}
	}

	if n.PendingRecursiveLevel != diffmodel.NotCompared {
		s.armRecursive(n.Children, n.PendingRecursiveLevel, n.PendingRecursivePriority)
	}
}

// OnCompareResult applies a completed comparison to the tree.
func (s *Session) OnCompareResult(r scan.CompareOutcome) {
	n, ok := s.nodeIndex[r.RelPath]
	if !ok {
		return
	}
	tree.ApplyCompareResult(n, r.Level, r.Result, r.Err, r.Stat)
}

// Navigate reprioritizes the listing queue for a directory change: to's
// own listing (if still pending) jumps to High, and its currently known
// child directories are boosted to Medium so drilling one level further
// is fast (SPEC.md §8.3). Moving the cursor within an already-listed
// directory should not call this — only changing the current directory
// does.
func (s *Session) Navigate(to *tree.Node) {
	if to == nil {
		return
	}
	if !to.Listed {
		if to.Listing {
			s.listQ.Boost(to.RelPath, workqueue.High)
		} else {
			s.enqueueList(to, workqueue.High)
		}
	}
	for _, c := range to.Children {
		if !c.IsDir() {
			continue
		}
		if !c.Listed {
			if c.Listing {
				s.listQ.Boost(c.RelPath, workqueue.Medium)
			} else {
				s.enqueueList(c, workqueue.Medium)
			}
		}
	}
}

// TriggerCompare starts a comparison at level for dir's children
// (SPEC.md §5.2). If recursive is false, only dir's direct file/symlink
// children are compared, at High priority. If recursive is true, the
// entire subtree rooted at dir is armed at Medium priority: already-known
// descendants are enqueued immediately, and any not yet discovered by
// the background listing scan are picked up as they're found (via
// OnListResult).
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
			dir.PendingRecursivePriority = workqueue.Medium
		}
		s.armRecursive(dir.Children, level, workqueue.Medium)
		return
	}
	for _, c := range dir.Children {
		if !c.IsDir() {
			s.maybeEnqueueCompare(c, level, workqueue.High)
		}
	}
}

// armRecursive marks each directory in nodes (and, transitively, every
// already-listed descendant directory) as pending level/prio, and
// enqueues compare jobs for every currently known file/symlink
// descendant.
func (s *Session) armRecursive(nodes []*tree.Node, level diffmodel.CompareLevel, prio workqueue.Priority) {
	for _, c := range nodes {
		if c.Presence != diffmodel.Both {
			continue // nothing to compare against on the missing side
		}
		if c.IsDir() {
			if level > c.PendingRecursiveLevel {
				c.PendingRecursiveLevel = level
				c.PendingRecursivePriority = prio
			}
			if c.Listed {
				s.armRecursive(c.Children, level, prio)
			}
			continue
		}
		s.maybeEnqueueCompare(c, level, prio)
	}
}

func (s *Session) maybeEnqueueCompare(n *tree.Node, level diffmodel.CompareLevel, prio workqueue.Priority) {
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
	s.cmpQ.Upsert(n.RelPath, prio, job, func(old scan.CompareJob) scan.CompareJob {
		if level > old.Level {
			old.Level = level
		}
		return old
	})
}

// CancelPendingCompares drops every not-yet-started queued comparison
// job (the global cancel key, SPEC.md §5.4). In-flight jobs already
// picked up by a worker finish normally.
func (s *Session) CancelPendingCompares() {
	s.cmpQ.Clear()
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
