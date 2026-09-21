// Package pairtree holds the merged state of one pairing: two
// directories — normally the two compared roots, but under a sub-compare
// any two — matched entry by entry, with comparison results and the
// SPEC.md §3.3 status rollup. A Node points at the two sidetree nodes it
// pairs rather than owning their names or metadata, so the same side
// node can appear in several pairings at once and listing is never
// repeated for any of them.
//
// Nodes are mutated exclusively from the UI's Bubble Tea Update loop (a
// single goroutine) as scan/compare results arrive as messages —
// nothing in this package does I/O or touches a worker pool, so it needs
// no locking.
package pairtree

import (
	"sort"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/sidetree"
)

// Node is one row: an entry matched (or unmatched) between this
// pairing's two sides. Left and Right point at the side nodes it pairs;
// either may be nil, which is what makes the row one-sided. A
// directory's Result works exactly like a file's — it's just computed
// differently: instead of coming from a compare job, it's rolled up as
// the worst status found among its Children (SPEC.md §3.3), recomputed
// whenever a child changes.
type Node struct {
	Left, Right *sidetree.Node

	Type diffmodel.EntryType
	// PairRel is the path of this row relative to the pairing's two
	// roots — the one path that names it on both sides at once. Each
	// side's own path below its own tree root is on its side node, and
	// under a sub-compare the three are all different.
	PairRel  string
	Parent   *Node
	Children []*Node

	// Level is the deepest CompareLevel actually run for a file/symlink
	// node. For a directory, it's rolled up instead (see rollupLevel):
	// the level shared by every descendant compared so far, or the
	// deepest one seen if they disagree — in which case LevelMixed is
	// also set, since no single level then describes the subtree.
	Level      diffmodel.CompareLevel
	LevelMixed bool
	Result     diffmodel.CompareResult
	Err        error

	// PendingRecursiveLevel records a recursive compare trigger (SPEC.md
	// §5.2/§5.4) that applies to this directory's subtree. It's consulted
	// whenever new children are discovered (via listing) so a recursive
	// compare started before the whole subtree is known still reaches
	// every descendant as it's found.
	PendingRecursiveLevel diffmodel.CompareLevel

	// ArmedLevel is a comparison asked for on this row that couldn't be
	// acted on yet because the row's metadata wasn't known. A content
	// comparison waits for both sides' sizes, since two files of
	// different lengths differ without a byte being read (SPEC.md §5.1)
	// — so the job is only ever created once the sizes are known to
	// match. Package session re-checks this whenever a stat result
	// lands, the same way PendingRecursiveLevel is re-checked whenever a
	// listing does.
	ArmedLevel diffmodel.CompareLevel

	// PendingCompare counts comparison jobs queued or in flight anywhere
	// in this node's subtree, including itself. Comparison only ever runs
	// against both-sided entries, so unlike listing — which each side
	// tracks in its own tree — one combined count is enough. Package
	// session keeps it in sync via AdjustPendingCompare as jobs are
	// enqueued and results applied, so an ancestor directory can tell
	// whether work is still outstanding anywhere beneath it — unlike
	// Result, which for a directory only ever reflects completed results
	// (SPEC.md §3.3).
	PendingCompare int

	// descMatches counts, per diffmodel.RowStatus, how many of this
	// node's descendants (not counting itself) currently have that
	// status. It answers the row filter's "is there a matching row
	// somewhere below?" question (SPEC.md §4.7) in O(1), which matters
	// because the UI asks it for every visible row on every render — a
	// subtree walk there costs a full traversal of the tree several
	// times a second, forever, even on an idle fully-scanned tree.
	//
	// Merge and ApplyCompareResult are what keep it correct: the first is
	// the only way a node is linked into the tree, the second the only
	// thing that can change a node's own status once linked (Presence and
	// Type are fixed at creation, and a directory's status never depends
	// on its rollup). The per-side entry counts and sizes the details
	// panel shows are not kept here at all — they're a property of one
	// side's subtree, not of how the two were matched, so a pair node
	// reads them straight off its side nodes (see SideTotals).
	descMatches [diffmodel.RowStatusCount]int32
}

// Presence reports which side(s) this row exists on. It's derived from
// the two side pointers rather than stored, and fixed once the node is
// created: Merge only builds a node's children once both its sides have
// been listed, so a row never changes the side it's on after the fact.
func (n *Node) Presence() diffmodel.Presence {
	switch {
	case n.Right == nil:
		return diffmodel.LeftOnly
	case n.Left == nil:
		return diffmodel.RightOnly
	default:
		return diffmodel.Both
	}
}

// Name is the entry's name, which both sides share: matching is by
// exact name and type (SPEC.md §3.1), so a both-sided row's two sides
// can't be named differently. Only a pairing's root pairs two
// differently-named directories, and it has no row of its own.
func (n *Node) Name() string {
	if n.Left != nil {
		return n.Left.Name
	}
	return n.Right.Name
}

func (n *Node) IsDir() bool { return n.Type == diffmodel.Dir }

// Listed reports whether every side this node actually has has been
// listed — the condition Merge gates on, and what "the current directory
// is still loading" means for the pane.
func (n *Node) Listed() bool {
	if n.Left != nil && !n.Left.Listed {
		return false
	}
	if n.Right != nil && !n.Right.Listed {
		return false
	}
	return true
}

// ListErrs reports each side's listing error, if any. A side this node
// doesn't have never failed to list — there was nothing to read.
func (n *Node) ListErrs() (left, right error) {
	if n.Left != nil {
		left = n.Left.ListErr
	}
	if n.Right != nil {
		right = n.Right.ListErr
	}
	return left, right
}

// PendingListing reports the listing work still outstanding in each
// side's subtree. Listing is tracked per side because each side's tree
// is listed independently — a one-sided descendant's listing job only
// ever does real work on the side it exists on, so it only ever shows as
// pending there.
func (n *Node) PendingListing() (left, right int) {
	if n.Left != nil {
		left = n.Left.PendingListing
	}
	if n.Right != nil {
		right = n.Right.PendingListing
	}
	return left, right
}

// ExaminePending reports whether any triggered examination is still
// outstanding for n or anywhere beneath it (SPEC.md §8.2): a content
// comparison in this pairing, or a metadata stat on either side. Both
// count, because both are work the user asked for and both can still
// change what this row says — so the row shows as pending until neither
// is left. Stat work is read off the side nodes rather than tallied
// here, since it's shared with every other pairing over the same files.
func (n *Node) ExaminePending() bool {
	if n.PendingCompare > 0 {
		return true
	}
	left, right := n.PendingStat()
	return left > 0 || right > 0
}

// PendingStat reports the metadata work still outstanding in each side's
// subtree, the per-side counterpart to PendingCompare.
func (n *Node) PendingStat() (left, right int) {
	if n.Left != nil {
		left = n.Left.PendingStat
	}
	if n.Right != nil {
		right = n.Right.PendingStat
	}
	return left, right
}

// SideTotals reports what each side of this directory's subtree holds
// (SPEC.md §4.2). A pair node's per-side totals are exactly its side
// node's own — what one side contains doesn't depend on how it was
// matched against the other — so nothing is aggregated per pairing here.
// A side the node doesn't have totals to zero; callers distinguish that
// from an empty subtree via Presence.
func (n *Node) SideTotals() (left, right sidetree.Totals) {
	if n.Left != nil {
		left = n.Left.Totals()
	}
	if n.Right != nil {
		right = n.Right.Totals()
	}
	return left, right
}

// RowStatus is n's own status for filtering purposes (SPEC.md §4.7),
// independent of anything below it — diffmodel.RowNone for a row that
// currently has no status at all.
func (n *Node) RowStatus() diffmodel.RowStatus {
	return diffmodel.ClassifyRow(n.Type, n.Presence(), n.Result)
}

// DescendantsWithStatus reports how many of n's descendants, at any
// depth, currently have status s. n itself is never counted, so a
// caller asking "should this row be shown under the filter?" tests the
// row's own RowStatus and this separately — which is what keeps a
// directory shown only because of what's beneath it distinguishable
// (and dimmable) from one that matches in its own right.
func (n *Node) DescendantsWithStatus(s diffmodel.RowStatus) int {
	if s < 0 || s >= diffmodel.RowStatusCount {
		return 0
	}
	return int(n.descMatches[s])
}

// statusDelta is a change to a subtree's per-status tallies.
type statusDelta [diffmodel.RowStatusCount]int32

func (d *statusDelta) add(other statusDelta) {
	for s, v := range other {
		d[s] += v
	}
}

func (d statusDelta) isZero() bool { return d == statusDelta{} }

func (d statusDelta) negated() statusDelta {
	var out statusDelta
	for s, v := range d {
		out[s] = -v
	}
	return out
}

// adjustTalliesUpward applies delta to n and every ancestor of n. Cost
// is the depth of the tree, not the size of the subtree — the same
// upward walk AdjustPendingCompare and recomputeResultUpward already do
// for every result that arrives.
func adjustTalliesUpward(n *Node, delta statusDelta) {
	if delta.isZero() {
		return
	}
	for cur := n; cur != nil; cur = cur.Parent {
		for s, v := range delta {
			cur.descMatches[s] += v
		}
	}
}

// AddChild links child — with whatever subtree it already carries —
// into parent's Children, and folds its statuses into the descendant
// tallies of parent and every ancestor. Assigning to Children directly
// leaves those tallies stale, so this is the only supported way to
// attach a node.
func AddChild(parent, child *Node) {
	adjustTalliesUpward(parent, linkChild(parent, child))
}

// linkChild is AddChild without the upward walk: it returns the delta
// the ancestors still need, so a caller attaching many children at once
// (Merge) can sum them and walk up a single time.
func linkChild(parent, child *Node) statusDelta {
	child.Parent = parent
	parent.Children = append(parent.Children, child)
	return contribution(child)
}

// contribution is what child accounts for in its ancestors' tallies:
// everything its own subtree already carries, plus its own status.
func contribution(child *Node) statusDelta {
	d := statusDelta(child.descMatches)
	d.add(ownContribution(child))
	return d
}

// ownContribution is what a single node counts for in its ancestors'
// tallies, ignoring anything below it: its own filterable status, if it
// has one.
func ownContribution(n *Node) statusDelta {
	var d statusDelta
	if s := n.RowStatus(); s != diffmodel.RowNone {
		d[s]++
	}
	return d
}

// childPairRel computes the pairing-relative path of a child named name
// under parent.
func childPairRel(parent *Node, name string) string {
	if parent.PairRel == "" {
		return name
	}
	return parent.PairRel + "/" + name
}

// childKey is what the two sides are matched by (SPEC.md §3.1): exact
// name and entry type, so a file and a directory of the same name never
// merge into one row.
type childKey struct {
	name string
	typ  diffmodel.EntryType
}

// Merge extends n's children with whatever its two sides hold and n
// doesn't have a row for yet, matching by (name, type), and returns the
// rows it added. It also recomputes n's rollup, since a listing that
// failed changes n's own Result whether or not it produced any entries.
//
// Children are built only once every side n actually has is Listed. The
// alternative — showing one side's entries immediately as one-sided and
// upgrading them to both-sided when the other side lists — would flash a
// screen of one-sided glyphs that then turn green, and would make a
// row's Presence mutable, which the tallies above would have to learn to
// track. Gating instead keeps Presence fixed at creation.
func Merge(n *Node) (added []*Node) {
	if !n.Listed() {
		recomputeResultUpward(n)
		return nil
	}

	have := make(map[childKey]bool, len(n.Children))
	for _, c := range n.Children {
		have[childKey{c.Name(), c.Type}] = true
	}

	type sides struct{ left, right *sidetree.Node }
	union := map[childKey]*sides{}
	var keys []childKey
	slot := func(k childKey) *sides {
		s, ok := union[k]
		if !ok {
			s = &sides{}
			union[k] = s
			keys = append(keys, k)
		}
		return s
	}
	if n.Left != nil {
		for _, c := range n.Left.Children {
			k := childKey{c.Name, c.Type}
			if !have[k] {
				slot(k).left = c
			}
		}
	}
	if n.Right != nil {
		for _, c := range n.Right.Children {
			k := childKey{c.Name, c.Type}
			if !have[k] {
				slot(k).right = c
			}
		}
	}

	var delta statusDelta
	for _, k := range keys {
		s := union[k]
		child := &Node{
			Left: s.left, Right: s.right,
			Type:    k.typ,
			PairRel: childPairRel(n, k.name),
		}
		delta.add(linkChild(n, child))
		added = append(added, child)
	}
	if len(added) > 0 {
		sortChildren(n.Children)
		adjustTalliesUpward(n, delta)
	}
	recomputeResultUpward(n)
	// Only once the rows are linked and counted: a row whose two sides
	// were statted before it existed — anything a second pairing covers
	// — already has its metadata verdict, for free.
	for _, c := range added {
		ApplyMetadata(c)
	}
	return added
}

// ApplyMetadata records the metadata-level verdict for n if both its
// sides have been statted, and does nothing otherwise. No job and no
// I/O: once each side's size and mtime are known, comparing them is an
// equality test, and it gives the same answer in every pairing over the
// same two files. A symlink is settled here for good rather than at a
// level — its target string is the whole of what can be compared
// (SPEC.md §7) — so it's recorded at the deepest level, which is also
// what keeps a later content trigger from queueing a job to open it.
func ApplyMetadata(n *Node) {
	if n.IsDir() || n.Presence() != diffmodel.Both {
		return
	}
	if !n.Left.HaveStat || !n.Right.HaveStat {
		return
	}
	if err := firstErr(n.Left.StatErr, n.Right.StatErr); err != nil {
		ApplyCompareResult(n, diffmodel.SizeMtime, diffmodel.CompareError, err)
		return
	}
	if n.Type == diffmodel.Symlink {
		ApplyCompareResult(n, diffmodel.Checksum, sameIf(n.Left.LinkTarget == n.Right.LinkTarget), nil)
		return
	}
	same := n.Left.Size == n.Right.Size && n.Left.Mtime.Equal(n.Right.Mtime)
	ApplyCompareResult(n, diffmodel.SizeMtime, sameIf(same), nil)
}

func sameIf(same bool) diffmodel.CompareResult {
	if same {
		return diffmodel.Same
	}
	return diffmodel.Differs
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func sortChildren(children []*Node) {
	sort.Slice(children, func(i, j int) bool {
		return diffmodel.EntryLess(children[i].Name(), children[i].Type, children[j].Name(), children[j].Type)
	})
}

// ApplyCompareResult records a comparison outcome for n. It's a no-op if
// level is not deeper than what's already known, so a stale or duplicate
// result can never downgrade a deeper one (SPEC.md §5.3 monotonicity).
// The size/mtime a comparison happened to read is not recorded here —
// that's metadata about one side's file, which belongs in that side's
// tree and is shared with every other pairing over it.
func ApplyCompareResult(n *Node, level diffmodel.CompareLevel, result diffmodel.CompareResult, cmpErr error) {
	if level <= n.Level {
		return
	}
	before := ownContribution(n)
	n.Level = level
	n.Result = result
	n.Err = cmpErr
	// A result is the only thing that can change what a row already in
	// the tree counts for: its status can move from not-yet-compared to
	// equal or differing (or equal to differing, when a deeper level
	// overrides a shallower verdict). The ancestors' tallies count
	// descendants only, so the delta starts at the parent.
	delta := before.negated()
	delta.add(ownContribution(n))
	adjustTalliesUpward(n.Parent, delta)
	recomputeResultUpward(n.Parent)
}

// AdjustPendingCompare changes n's PendingCompare count by delta and
// propagates the same change up through Parent to the root.
func AdjustPendingCompare(n *Node, delta int) {
	for cur := n; cur != nil; cur = cur.Parent {
		cur.PendingCompare += delta
	}
}

// recomputeResultUpward recomputes n's Result — a directory's rolled up
// from its current Children — and propagates upward until reaching the
// pairing's root. n is always a directory: the only callers are Merge
// (passing the directory just merged) and ApplyCompareResult (passing
// the compared node's parent), so this never overwrites a file's Result.
func recomputeResultUpward(n *Node) {
	for n != nil {
		n.Result = rollupResult(n)
		n.Level, n.LevelMixed = rollupLevel(n)
		n = n.Parent
	}
}

// rollupResult computes a directory's own Result as the worst status
// found among its children (SPEC.md §3.3): since each child's own
// Result already reflects its own rollup if it's a directory, a single
// pass over direct children is enough — no separate recursion needed.
func rollupResult(n *Node) diffmodel.CompareResult {
	if left, right := n.ListErrs(); left != nil || right != nil {
		return diffmodel.CompareError
	}
	// A directory whose entries aren't all known yet says nothing about
	// its subtree, which is exactly what Unknown means. Merge recomputes
	// the rollup even while it's still gated on the other side's listing
	// — a failed listing changes the verdict whether or not any entries
	// came back — so this is reached in earnest, not just defensively.
	if !n.Listed() {
		return diffmodel.Unknown
	}
	// A directory listed on both sides with no children at all has
	// nothing that could differ — vacuously Same, not Unknown, which
	// would otherwise never resolve for an empty directory, since
	// there's nothing to trigger a compare on.
	if len(n.Children) == 0 {
		return diffmodel.Same
	}
	// Same is the fold's bottom sentinel, not Unknown: resultRank ranks
	// Unknown above Same, so starting from Unknown would make the loop
	// stick there even once every real child comes back Same.
	worst := diffmodel.Same
	for _, c := range n.Children {
		worst = worstResult(worst, ownStatus(c))
	}
	return worst
}

// ownStatus is a child's own status for rollup purposes: a one-sided
// entry counts as "differs" (SPEC.md §3.3 rolls up presence issues, not
// just compare results); otherwise it's just the child's Result, which
// for a directory child is already its own rollup.
func ownStatus(n *Node) diffmodel.CompareResult {
	switch n.Presence() {
	case diffmodel.LeftOnly, diffmodel.RightOnly:
		return diffmodel.Differs
	}
	return n.Result
}

// rollupLevel computes a directory's own "compared by" level from its
// children, the same way rollupResult computes Result: a child directory
// already carries its own rolled-up Level/LevelMixed, so one pass over
// direct children is enough. A child not yet compared (Level ==
// NotCompared) is excluded rather than treated as a disagreement, since
// "hasn't run yet" isn't a level the subtree was actually compared at —
// unlike rollupResult, where an uncompared child does pull the aggregate
// down (to Unknown), since Result answers "can we call this Same yet?"
// while Level only describes the levels actually used so far. A
// one-sided child has no comparison to report and is excluded too.
func rollupLevel(n *Node) (diffmodel.CompareLevel, bool) {
	level := diffmodel.NotCompared
	mixed := false
	for _, c := range n.Children {
		var cLevel diffmodel.CompareLevel
		switch {
		case c.IsDir():
			cLevel = c.Level
			if c.LevelMixed {
				mixed = true
			}
		case c.Presence() == diffmodel.Both:
			cLevel = c.Level
		default:
			continue
		}
		if cLevel == diffmodel.NotCompared {
			continue
		}
		switch {
		case level == diffmodel.NotCompared:
			level = cLevel
		case cLevel != level:
			mixed = true
			if cLevel > level {
				level = cLevel
			}
		}
	}
	return level, mixed
}

// resultRank orders CompareResult for rollup purposes only — not the
// same ordering as diffmodel.CompareResult's own iota values. Unknown
// (not yet compared) outranks Same: a directory can't be declared Same
// while any child is still uncompared, since that child could yet turn
// out to differ. Differs/CompareError outrank everything, since a single
// confirmed difference makes the rest moot.
var resultRank = map[diffmodel.CompareResult]int{
	diffmodel.Same:         0,
	diffmodel.Unknown:      1,
	diffmodel.Differs:      2,
	diffmodel.CompareError: 2,
}

func worstResult(a, b diffmodel.CompareResult) diffmodel.CompareResult {
	if resultRank[b] > resultRank[a] {
		return b
	}
	return a
}
