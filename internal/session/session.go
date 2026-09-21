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
	"strings"
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

// examineJob is one unit of triggered examination (SPEC.md §8.2), the
// two kinds sharing a pool: reading one side's metadata, or reading both
// sides of a file to compare them. They share a pool because they're the
// same kind of work from the user's point of view — I/O asked for by a
// trigger, which 'x' cancels — as opposed to the ambient listing that
// keeps navigation responsive.
type examineJob struct {
	isStat  bool
	stat    scan.StatJob
	compare scan.CompareJob
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

	listQ    *workqueue.Queue[scan.ListJob]
	examineQ *workqueue.Queue[examineJob]

	listResults chan scan.ListResult
	statResults chan scan.StatResult
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
		listQ:    workqueue.New[scan.ListJob](),
		examineQ: workqueue.New[examineJob](),
		// Buffered so workers never block handing off a result while the
		// UI is busy processing the previous one.
		listResults: make(chan scan.ListResult, 64),
		statResults: make(chan scan.StatResult, 64),
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

// ListResults, StatResults and CompareResults are consumed by package ui
// to build long-lived listening tea.Cmds.
func (s *Session) ListResults() <-chan scan.ListResult        { return s.listResults }
func (s *Session) StatResults() <-chan scan.StatResult        { return s.statResults }
func (s *Session) CompareResults() <-chan scan.CompareOutcome { return s.cmpResults }

// Close shuts down both worker pools. Safe to call once, e.g. after the
// Bubble Tea program exits.
func (s *Session) Close() {
	s.listQ.Close()
	s.examineQ.Close()
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

func (s *Session) examineWorkerLoop() {
	for {
		job, key, ok := s.examineQ.PopUnless(func() bool { return shrinkIfExcess(&s.cmpLive, &s.cmpTarget) })
		if !ok {
			return
		}
		if job.isStat {
			result := scan.DoStat(job.stat)
			s.examineQ.Done(key)
			s.statResults <- result
			continue
		}
		outcome := scan.DoCompare(job.compare)
		s.examineQ.Done(key)
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
			go s.examineWorkerLoop()
		}
	case int32(n) < old:
		s.examineQ.Wake()
	}
}

// ListWorkers and CompareWorkers report each pool's currently configured
// size (not how many of its goroutines happen to be live at this exact
// instant — see listTarget's doc comment), for the UI's 'w' popup to
// display and prefill for editing.
func (s *Session) ListWorkers() int    { return int(s.listTarget.Load()) }
func (s *Session) CompareWorkers() int { return int(s.cmpTarget.Load()) }

// Job keys are namespaced by a leading path segment naming which tree
// the rest of the key is a path in (SPEC.md §8.3), so the queue's own
// distance math applies to them unchanged and the focus of one pane
// never reorders the other's work. Per-side work — listing and metadata
// — is keyed "L/…"/"R/…"; a content comparison belongs to a pairing
// rather than to a side, and is keyed by it.
//
// Listing and metadata can share a spelling because they live in
// different queues, and within the examination queue a side key can
// never collide with a pairing key.
const rootPairingNS = "p"

func sideKey(sd diffmodel.Side, relPath string) string {
	ns := "L"
	if sd == diffmodel.Right {
		ns = "R"
	}
	return joinKey(ns, relPath)
}

func pairKey(relPath string) string { return joinKey(rootPairingNS, relPath) }

func joinKey(ns, relPath string) string {
	if relPath == "" {
		return ns
	}
	return ns + "/" + relPath
}

func (s *Session) enqueueList(sd diffmodel.Side, n *sidetree.Node) {
	n.Listing = true
	created := s.listQ.Upsert(sideKey(sd, n.RelPath), scan.ListJob{
		Side:    sd,
		RelPath: n.RelPath,
		Abs:     scan.AbsPath(s.sides[sd].root, n.RelPath),
	}, nil)
	if created {
		sidetree.AdjustPendingListing(n, 1)
	}
}

// enqueueStat asks for n's metadata, unless it's already known. It runs
// on the examination pool rather than the listing pool (SPEC.md §8.2):
// a stat is triggered work — the product of 'c' or of --level's ambient
// arming — so 'x' must be able to cancel it, and it must not compete
// with the ambient listing that keeps navigation responsive.
func (s *Session) enqueueStat(sd diffmodel.Side, n *sidetree.Node) {
	if n.HaveStat || n.IsDir() {
		return
	}
	created := s.examineQ.Upsert(sideKey(sd, n.RelPath), examineJob{stat: scan.StatJob{
		Side:    sd,
		RelPath: n.RelPath,
		Abs:     scan.AbsPath(s.sides[sd].root, n.RelPath),
		Type:    n.Type,
	}, isStat: true}, nil)
	if created {
		sidetree.AdjustPendingStat(n, 1)
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

// OnStatResult applies metadata to the side tree it came from and fans
// it out to the pairing, exactly the way a listing does — filling in
// sizes instead of children. Once both sides of a row are known, its
// metadata verdict falls out in memory, and a content comparison armed
// on it becomes answerable: either the two sizes differ, which settles
// it without opening a file, or they match and the job is finally worth
// creating (SPEC.md §5.1).
func (s *Session) OnStatResult(r scan.StatResult) {
	n, ok := s.sides[r.Side].tree.Index[r.RelPath]
	if !ok {
		return
	}
	sidetree.ApplyStat(n, sidetree.Stat{Size: r.Size, Mtime: r.Mtime, LinkTarget: r.LinkTarget, Err: r.Err})
	sidetree.AdjustPendingStat(n, -1)

	p, ok := s.pairOf[r.Side][n]
	if !ok {
		return
	}
	pairtree.ApplyMetadata(p)
	if p.ArmedLevel != diffmodel.NotCompared {
		s.examine(p, p.ArmedLevel)
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

// OnCompareResult applies a completed content comparison to the
// pairing. It carries no metadata: a size is a fact about one side's
// file, which a stat job reads once into that side's tree and every
// pairing over it then shares.
func (s *Session) OnCompareResult(r scan.CompareOutcome) {
	n, ok := s.Node(r.RelPath)
	if !ok {
		return
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
		foci = append(foci, sideKey(sd, sn.RelPath))
	}
	s.listQ.SetFoci(foci...)
	// The examination queue holds both namespaces at once — per-side
	// stats and this pairing's content jobs — so it gets all three foci.
	s.examineQ.SetFoci(append(foci, pairKey(to.RelPath))...)
}

// TriggerCompare starts a comparison at level for target (SPEC.md §5.2).
// A file or symlink target is compared on its own, and recursive means
// nothing for it. A directory target compares its direct file/symlink
// children, or — when recursive — its entire subtree: already-known
// descendants are enqueued immediately, and any not yet discovered by
// the background listing scan are picked up as they're found (via
// OnListResult). Either way, target is at or under the current
// navigation focus, so these jobs already sort ahead of unrelated
// background work (SPEC.md §8.3) without needing a priority of their own.
func (s *Session) TriggerCompare(target *pairtree.Node, level diffmodel.CompareLevel, recursive bool) {
	if target == nil {
		return
	}
	if !target.IsDir() {
		s.examine(target, level)
		return
	}
	dir := target
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
			s.examine(c, level)
		}
	}
}

// armRecursive marks each directory in nodes (and, transitively, every
// already-listed descendant directory) as pending level, and examines
// every currently known file/symlink descendant.
//
// One-sided entries are included, unlike the comparisons they can never
// take part in: a metadata read has a counterpart-free half that's worth
// doing on its own, which is what finally lets a one-sided subtree
// report a size instead of "size ?" forever (SPEC.md §4.2).
func (s *Session) armRecursive(nodes []*pairtree.Node, level diffmodel.CompareLevel) {
	for _, c := range nodes {
		if c.IsDir() {
			if level > c.PendingRecursiveLevel {
				c.PendingRecursiveLevel = level
			}
			if c.Listed() {
				s.armRecursive(c.Children, level)
			}
			continue
		}
		s.examine(c, level)
	}
}

// examine enqueues whatever work level actually needs for one row.
//
// Every level starts with metadata, on each side the row exists — a
// stat is the only way a one-sided entry is ever measured, and both
// sides' sizes are what let a content comparison decide whether to read
// anything at all. If the two sizes are already known to differ, that
// *is* the content verdict (SPEC.md §5.1) and no job is created; only
// size-matching files ever reach the compare pool. A row whose metadata
// isn't in yet records what it was asked for and is re-examined when
// its stat results land.
func (s *Session) examine(n *pairtree.Node, level diffmodel.CompareLevel) {
	if n.IsDir() || n.Level >= level {
		return // already have an equal-or-deeper result (SPEC.md §5.3)
	}
	for _, sd := range diffmodel.Sides {
		if sn := sideNode(n, sd); sn != nil {
			s.enqueueStat(sd, sn)
		}
	}
	if level < diffmodel.Checksum || n.Presence() != diffmodel.Both {
		return // nothing further: a metadata verdict needs no job at all
	}
	if !n.Left.HaveStat || !n.Right.HaveStat {
		n.ArmedLevel = level
		return
	}
	n.ArmedLevel = diffmodel.NotCompared
	if n.Left.StatErr == nil && n.Right.StatErr == nil && n.Left.Size != n.Right.Size {
		pairtree.ApplyCompareResult(n, level, diffmodel.Differs, nil)
		return
	}
	s.enqueueCompare(n, level)
}

func (s *Session) enqueueCompare(n *pairtree.Node, level diffmodel.CompareLevel) {
	job := examineJob{compare: scan.CompareJob{
		RelPath:  n.RelPath,
		LeftAbs:  scan.AbsPath(s.sides[diffmodel.Left].root, n.Left.RelPath),
		RightAbs: scan.AbsPath(s.sides[diffmodel.Right].root, n.Right.RelPath),
		Type:     n.Type,
		Level:    level,
	}}
	created := s.examineQ.Upsert(pairKey(n.RelPath), job, func(old examineJob) examineJob {
		if level > old.compare.Level {
			old.compare.Level = level
		}
		return old
	})
	if created {
		pairtree.AdjustPendingCompare(n, 1)
	}
}

// CancelPendingCompares drops every not-yet-started queued examination —
// metadata as well as content, both being work a trigger asked for (the
// global cancel key, SPEC.md §5.4). In-flight jobs already picked up by
// a worker finish normally. Dropped jobs will now never produce a
// result, so their subtree pending counts are unwound here instead of
// via OnStatResult/OnCompareResult; which counter that is follows from
// the key's namespace.
func (s *Session) CancelPendingCompares() {
	for _, key := range s.examineQ.Clear() {
		ns, rel, _ := strings.Cut(key, "/")
		if ns == rootPairingNS {
			if n, ok := s.Node(rel); ok {
				pairtree.AdjustPendingCompare(n, -1)
			}
			continue
		}
		sd := diffmodel.Left
		if ns == "R" {
			sd = diffmodel.Right
		}
		if sn, ok := s.sides[sd].tree.Index[rel]; ok {
			sidetree.AdjustPendingStat(sn, -1)
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
		CmpPending: s.examineQ.PendingCount(), CmpActive: s.examineQ.ActiveCount(),
	}
}

// IsListPending reports whether relPath's listing on one side is queued
// or in-flight.
func (s *Session) IsListPending(sd diffmodel.Side, relPath string) bool {
	return s.listQ.IsPending(sideKey(sd, relPath))
}
