// Package session orchestrates dirdiff's background work: it owns the
// two side trees and the pairing over them, the two worker pools
// (listing and comparison, SPEC.md §8.2), and the logic that decides
// what to enqueue and at what priority. It is deliberately independent
// of Bubble Tea — it exposes plain channels for results, which package
// ui wraps into tea.Cmd/tea.Msg. All tree mutation happens on the
// caller's goroutine (the UI's Update loop); Session itself only touches
// the concurrency-safe worker queues.
package session

import (
	"sync/atomic"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/pairtree"
	"github.com/m42cel/dirdiff/internal/scan"
	"github.com/m42cel/dirdiff/internal/sidetree"
	"github.com/m42cel/dirdiff/internal/workqueue"
)

// side is one compared tree: where it lives on disk and what's been
// discovered of it so far.
type side struct {
	root string
	tree *sidetree.Tree
}

// Session holds everything needed to scan and compare two directory
// trees in the background.
type Session struct {
	LeftRoot, RightRoot string

	// sides holds the two trees, indexed by diffmodel.Side. Listing and
	// metadata land here and are shared: a subtree read once is read once,
	// however many pairings end up covering it.
	sides [2]*side

	// Tree is the root pairing: the two roots matched entry by entry.
	Tree *pairtree.Node

	// pairOf maps a side node to the row pairing it, per side, so a
	// listing result can be fanned out from the side tree it landed in to
	// the pairing that shows it in O(1). A side node appears in at most
	// one row of a given pairing.
	pairOf [2]map[*sidetree.Node]*pairtree.Node

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
// initial listing of each root so the first level is shown as soon as
// possible (SPEC.md §8.1). If autoLevel is not NotCompared, the whole
// tree is armed to auto-compare recursively at that level in the
// background as listing discovers it (SPEC.md §2.1's --level flag,
// metadata by default).
//
// listWorkers and compareWorkers size the two pools independently
// (SPEC.md §8.2's --scan-workers/--compare-workers): listing is cheap,
// low-CPU directory-metadata I/O that doesn't benefit much from scaling
// with core count (and on a mechanical disk, more concurrent listing
// jobs can mean more seeking for no throughput gain) — though it wants
// at least two, since a listing job reads one side and the two sides of
// a directory are otherwise read one after the other. Comparison —
// especially at the content level — does real per-byte CPU work
// alongside the I/O, so scaling it with GOMAXPROCS is the more
// defensible default of the two.
func New(leftRoot, rightRoot string, listWorkers, compareWorkers int, autoLevel diffmodel.CompareLevel) *Session {
	left, right := sidetree.NewTree(diffmodel.Left), sidetree.NewTree(diffmodel.Right)
	root := pairtree.NewRoot(left.Root, right.Root)
	s := &Session{
		LeftRoot: leftRoot, RightRoot: rightRoot,
		sides: [2]*side{
			diffmodel.Left:  {root: leftRoot, tree: left},
			diffmodel.Right: {root: rightRoot, tree: right},
		},
		Tree: root,
		pairOf: [2]map[*sidetree.Node]*pairtree.Node{
			diffmodel.Left:  {left.Root: root},
			diffmodel.Right: {right.Root: root},
		},
		listQ: workqueue.New[scan.ListJob](),
		cmpQ:  workqueue.New[scan.CompareJob](),
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

	for _, sd := range diffmodel.Sides {
		s.enqueueList(sd, s.sides[sd].tree.Root)
	}

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

// listKey namespaces a listing job by the side it reads (SPEC.md §8.3).
// The two side trees are unconnected, so the same relative path names two
// different jobs; the namespace is a leading path segment so the queue's
// own distance math applies to it unchanged.
func listKey(sd diffmodel.Side, relPath string) string {
	ns := "L"
	if sd == diffmodel.Right {
		ns = "R"
	}
	if relPath == "" {
		return ns
	}
	return ns + "/" + relPath
}

func (s *Session) enqueueList(sd diffmodel.Side, n *sidetree.Node) {
	n.Listing = true
	created := s.listQ.Upsert(listKey(sd, n.RelPath), scan.ListJob{
		Side:    sd,
		RelPath: n.RelPath,
		Abs:     scan.AbsPath(s.sides[sd].root, n.RelPath),
	}, nil)
	if created {
		sidetree.AdjustPendingListing(n, 1)
	}
}

// Node looks up the pairing row at relPath, if it's been discovered yet.
// A file and a directory of the same name are two unrelated rows sharing
// one path (SPEC.md §3.1) — only one of the two can be both-sided, and
// only then, so this resolves a compare job's path unambiguously; asked
// for a colliding path it answers with the left side's row.
func (s *Session) Node(relPath string) (*pairtree.Node, bool) {
	for _, sd := range diffmodel.Sides {
		sn, ok := s.sides[sd].tree.Index[relPath]
		if !ok {
			continue
		}
		if p, ok := s.pairOf[sd][sn]; ok {
			return p, true
		}
	}
	return nil, false
}

// OnListResult applies a completed listing to the side tree it came
// from, continues the ambient breadth-first scan into any newly
// discovered subdirectories of that side (SPEC.md §8.1), and then fans
// the new entries out to the pairing: whatever now has a counterpart on
// the other side becomes a row, and a directory armed for a recursive
// compare that started before it was listed has that comparison applied
// to its new children now (SPEC.md §5.2/§5.4).
func (s *Session) OnListResult(r scan.ListResult) {
	t := s.sides[r.Side].tree
	n, ok := t.Index[r.RelPath]
	if !ok {
		return
	}
	t.ApplyListing(n, r.Entries, r.Err)
	sidetree.AdjustPendingListing(n, -1)

	for _, c := range n.Children {
		if c.IsDir() {
			s.enqueueList(r.Side, c)
		}
	}

	p, ok := s.pairOf[r.Side][n]
	if !ok {
		return
	}
	s.merge(p)
	if p.PendingRecursiveLevel != diffmodel.NotCompared {
		s.armRecursive(p.Children, p.PendingRecursiveLevel)
	}
}

// merge builds whatever rows p's two sides now allow and indexes them,
// descending into any new directory row whose own two sides happen to be
// listed already — a row created after its subtree was scanned has no
// listing result left to arrive and would otherwise stay empty forever.
func (s *Session) merge(p *pairtree.Node) {
	for _, c := range pairtree.Merge(p) {
		for _, sd := range diffmodel.Sides {
			if sn := sideNode(c, sd); sn != nil {
				s.pairOf[sd][sn] = c
			}
		}
		if c.IsDir() {
			s.merge(c)
		}
	}
}

func sideNode(p *pairtree.Node, sd diffmodel.Side) *sidetree.Node {
	if sd == diffmodel.Right {
		return p.Right
	}
	return p.Left
}

// OnCompareResult applies a completed comparison: the verdict to the
// pairing, and any metadata it had to read to the side trees, where it's
// shared with every other pairing over the same files. Metadata is
// recorded even when the verdict itself is stale (SPEC.md §5.3 keeps the
// deeper level), since a size is a fact about the file rather than part
// of the comparison.
func (s *Session) OnCompareResult(r scan.CompareOutcome) {
	n, ok := s.Node(r.RelPath)
	if !ok {
		return
	}
	if r.Stat != nil {
		if n.Left != nil {
			sidetree.ApplyStat(n.Left, r.Stat.LeftSize, r.Stat.LeftMtime)
		}
		if n.Right != nil {
			sidetree.ApplyStat(n.Right, r.Stat.RightSize, r.Stat.RightMtime)
		}
	}
	pairtree.ApplyCompareResult(n, r.Level, r.Result, r.Err)
	pairtree.AdjustPendingCompare(n, -1)
}

// Navigate reprioritizes both worker queues for a directory change: to's
// two sides become the new focus paths (SPEC.md §8.3), so every
// still-queued job anywhere in either side's subtree — at any depth, not
// just its direct children — now pops ahead of everything outside it,
// ordered by tree-edge distance. Moving the cursor within an
// already-listed directory should not call this — only changing the
// current directory does.
func (s *Session) Navigate(to *pairtree.Node) {
	if to == nil {
		return
	}
	var foci []string
	for _, sd := range diffmodel.Sides {
		sn := sideNode(to, sd)
		if sn == nil {
			continue
		}
		if !sn.Listed && !sn.Listing {
			s.enqueueList(sd, sn)
		}
		foci = append(foci, listKey(sd, sn.RelPath))
	}
	s.listQ.SetFoci(foci...)
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
func (s *Session) TriggerCompare(dir *pairtree.Node, level diffmodel.CompareLevel, recursive bool) {
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
func (s *Session) armRecursive(nodes []*pairtree.Node, level diffmodel.CompareLevel) {
	for _, c := range nodes {
		if c.Presence() != diffmodel.Both {
			continue // nothing to compare against on the missing side
		}
		if c.IsDir() {
			if level > c.PendingRecursiveLevel {
				c.PendingRecursiveLevel = level
			}
			if c.Listed() {
				s.armRecursive(c.Children, level)
			}
			continue
		}
		s.maybeEnqueueCompare(c, level)
	}
}

func (s *Session) maybeEnqueueCompare(n *pairtree.Node, level diffmodel.CompareLevel) {
	if n.Presence() != diffmodel.Both {
		return
	}
	if n.Level >= level {
		return // already have an equal-or-deeper result (SPEC.md §5.3)
	}
	job := scan.CompareJob{
		RelPath:  n.RelPath,
		LeftAbs:  scan.AbsPath(s.sides[diffmodel.Left].root, n.Left.RelPath),
		RightAbs: scan.AbsPath(s.sides[diffmodel.Right].root, n.Right.RelPath),
		Type:     n.Type,
		Level:    level,
	}
	created := s.cmpQ.Upsert(n.RelPath, job, func(old scan.CompareJob) scan.CompareJob {
		if level > old.Level {
			old.Level = level
		}
		return old
	})
	if created {
		pairtree.AdjustPendingCompare(n, 1)
	}
}

// CancelPendingCompares drops every not-yet-started queued comparison
// job (the global cancel key, SPEC.md §5.4). In-flight jobs already
// picked up by a worker finish normally. Dropped jobs will now never
// produce a result, so their subtree pending counts are unwound here
// instead of via OnCompareResult.
func (s *Session) CancelPendingCompares() {
	for _, key := range s.cmpQ.Clear() {
		if n, ok := s.Node(key); ok {
			pairtree.AdjustPendingCompare(n, -1)
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

// IsListPending reports whether relPath's listing on one side is queued
// or in-flight.
func (s *Session) IsListPending(sd diffmodel.Side, relPath string) bool {
	return s.listQ.IsPending(listKey(sd, relPath))
}

// IsComparePending reports whether relPath has a queued or in-flight
// comparison job.
func (s *Session) IsComparePending(relPath string) bool { return s.cmpQ.IsPending(relPath) }
