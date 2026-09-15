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
	"github.com/m42cel/dirdiff/internal/workqueue"
)

// Node is one row: a name matched (or unmatched) between the left and
// right trees at RelPath. Directories additionally track their children
// once listed, and Rollup summarizes descendant status (SPEC.md §3.3).
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
	Rollup                    diffmodel.CompareResult

	// PendingRecursiveLevel/Priority records a recursive compare trigger
	// (SPEC.md §5.2/§5.4) that applies to this directory's subtree. It's
	// consulted whenever new children are discovered (via listing) so a
	// recursive compare started before the whole subtree is known still
	// reaches every descendant as it's found.
	PendingRecursiveLevel    diffmodel.CompareLevel
	PendingRecursivePriority workqueue.Priority
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
// tree state and recomputes the rollup.
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
	recomputeRollupUpward(n)
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
	recomputeRollupUpward(n.Parent)
}

// recomputeRollupUpward recomputes n's Rollup from its current Children
// and propagates upward until reaching the root.
func recomputeRollupUpward(n *Node) {
	for n != nil {
		n.Rollup = computeRollup(n)
		n = n.Parent
	}
}

func computeRollup(n *Node) diffmodel.CompareResult {
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
		if c.IsDir() {
			worst = worstResult(worst, c.Rollup)
		}
	}
	return worst
}

// ownStatus is a child's own comparison status for rollup purposes: a
// one-sided entry counts as "differs" (SPEC.md §3.3 rolls up presence
// issues, not just compare results).
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
