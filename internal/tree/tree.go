// Package tree holds the mutable state of the compared directory trees:
// one Node per matched (or unmatched) entry, with comparison results and
// the SPEC.md §3.3 status rollup. Nodes are mutated exclusively from the
// UI's Bubble Tea Update loop (a single goroutine) as scan/compare
// results arrive as messages — nothing in this package does I/O or
// touches a worker pool, so it needs no locking.
package tree

import (
	"sort"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
)

// Node is one row: a name matched (or unmatched) between the left and
// right trees at RelPath. A directory's Result works exactly like a
// file's — it's just computed differently: instead of coming from a
// compare job, it's rolled up as the worst status found among its
// Children (SPEC.md §3.3), recomputed whenever a child changes.
type Node struct {
	Name     string
	Type     diffmodel.EntryType
	Presence diffmodel.Presence
	RelPath  string
	Parent   *Node

	Level  diffmodel.CompareLevel
	Result diffmodel.CompareResult
	Err    error

	HaveStat              bool
	LeftSize, RightSize   int64
	LeftMtime, RightMtime time.Time

	// Directory-only fields.
	Listed                    bool
	Listing                   bool
	ListErrLeft, ListErrRight error
	Children                  []*Node

	// PendingRecursiveLevel records a recursive compare trigger (SPEC.md
	// §5.2/§5.4) that applies to this directory's subtree. It's consulted
	// whenever new children are discovered (via listing) so a recursive
	// compare started before the whole subtree is known still reaches
	// every descendant as it's found.
	PendingRecursiveLevel diffmodel.CompareLevel

	// PendingListingLeft/Right and PendingCompare count listing/
	// comparison jobs queued or in flight anywhere in this node's
	// subtree, including itself. Listing is tracked per side because
	// each side's tree is listed independently — a one-sided
	// descendant's listing job only ever does real work on the side it
	// exists on, so it should only ever show as pending on that side.
	// Compare only ever runs against Both-sided entries, so one combined
	// count is enough there. Package session keeps all three in sync via
	// AdjustPendingListing/AdjustPendingCompare as jobs are enqueued and
	// results applied, so an ancestor directory can tell whether work is
	// still outstanding anywhere beneath it — unlike Result, which for a
	// directory only ever reflects completed results (SPEC.md §3.3).
	PendingListingLeft  int
	PendingListingRight int
	PendingCompare      int
}

// NewRoot creates the root node of the tree, representing "" (the
// compared directories themselves), which always exists on both sides
// (main.go validates this before the TUI starts).
func NewRoot() *Node {
	return &Node{Type: diffmodel.Dir, Presence: diffmodel.Both, RelPath: ""}
}

func (n *Node) IsDir() bool { return n.Type == diffmodel.Dir }

// ChildRelPath computes the RelPath of a child named name under parent.
func ChildRelPath(parent *Node, name string) string {
	if parent.RelPath == "" {
		return name
	}
	return parent.RelPath + "/" + name
}

// ApplyListing records the result of listing n (a directory) and builds
// its Children. It does not enqueue any follow-up work — that requires
// the worker queues, which live in package session; this only mutates
// tree state and recomputes n's rolled-up Result.
func ApplyListing(n *Node, children []diffmodel.ListedChild, leftErr, rightErr error) {
	n.Listed = true
	n.Listing = false
	n.ListErrLeft, n.ListErrRight = leftErr, rightErr

	n.Children = make([]*Node, 0, len(children))
	for _, c := range children {
		n.Children = append(n.Children, &Node{
			Name: c.Name, Type: c.Type, Presence: c.Presence,
			RelPath: ChildRelPath(n, c.Name), Parent: n,
		})
	}
	sortChildren(n.Children)
	recomputeResultUpward(n)
}

func sortChildren(children []*Node) {
	sort.Slice(children, func(i, j int) bool {
		if (children[i].Type == diffmodel.Dir) != (children[j].Type == diffmodel.Dir) {
			return children[i].Type == diffmodel.Dir
		}
		return children[i].Name < children[j].Name
	})
}

// ApplyCompareResult records a comparison outcome for n. It's a no-op on
// Level/Result if level is not deeper than what's already known, so a
// stale or duplicate result can never downgrade a deeper one (SPEC.md
// §5.3 monotonicity) — but stat metadata (size/mtime) is always recorded
// when present, since it's informational for the details panel rather
// than part of the comparison verdict.
func ApplyCompareResult(n *Node, level diffmodel.CompareLevel, result diffmodel.CompareResult, cmpErr error, stat *diffmodel.StatInfo) {
	if level > n.Level {
		n.Level = level
		n.Result = result
		n.Err = cmpErr
	}
	if stat != nil {
		n.HaveStat = true
		n.LeftSize, n.RightSize = stat.LeftSize, stat.RightSize
		n.LeftMtime, n.RightMtime = stat.LeftMtime, stat.RightMtime
	}
	recomputeResultUpward(n.Parent)
}

// AdjustPendingListing changes n's PendingListingLeft/Right counts by
// leftDelta/rightDelta respectively and propagates the same change up
// through Parent to the root.
func AdjustPendingListing(n *Node, leftDelta, rightDelta int) {
	for cur := n; cur != nil; cur = cur.Parent {
		cur.PendingListingLeft += leftDelta
		cur.PendingListingRight += rightDelta
	}
}

// AdjustPendingCompare is AdjustPendingListing for comparison jobs.
func AdjustPendingCompare(n *Node, delta int) {
	for cur := n; cur != nil; cur = cur.Parent {
		cur.PendingCompare += delta
	}
}

// recomputeResultUpward recomputes n's Result — a directory's rolled up
// from its current Children — and propagates upward until reaching the
// root. n is always a directory: the only callers are ApplyListing
// (passing the directory just listed) and ApplyCompareResult (passing
// the compared node's parent), so this never overwrites a file's Result.
func recomputeResultUpward(n *Node) {
	for n != nil {
		n.Result = rollupResult(n)
		n = n.Parent
	}
}

// rollupResult computes a directory's own Result as the worst status
// found among its children (SPEC.md §3.3): since each child's own
// Result already reflects its own rollup if it's a directory, a single
// pass over direct children is enough — no separate recursion needed.
func rollupResult(n *Node) diffmodel.CompareResult {
	if n.ListErrLeft != nil || n.ListErrRight != nil {
		return diffmodel.CompareError
	}
	// A directory listed on both sides with no children at all has
	// nothing that could differ — vacuously Same, not Unknown (which
	// means "not evaluated yet" and would otherwise never resolve for
	// an empty directory, since there's nothing to trigger a compare on).
	if n.Listed && len(n.Children) == 0 {
		return diffmodel.Same
	}
	worst := diffmodel.Unknown
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
	switch n.Presence {
	case diffmodel.LeftOnly, diffmodel.RightOnly:
		return diffmodel.Differs
	}
	return n.Result
}

var resultRank = map[diffmodel.CompareResult]int{
	diffmodel.Unknown:      0,
	diffmodel.Same:         1,
	diffmodel.Differs:      2,
	diffmodel.CompareError: 2,
}

func worstResult(a, b diffmodel.CompareResult) diffmodel.CompareResult {
	if resultRank[b] > resultRank[a] {
		return b
	}
	return a
}
