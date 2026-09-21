// Package sidetree holds one side's own view of its directory tree: one
// Node per real filesystem entry, filled in by listing and metadata
// results for that side alone. It knows nothing about the other side or
// about how the two are matched — that's package pairtree's job — so the
// same node can be part of any number of pairings at once, and listing a
// directory is never repeated for a second pairing over it.
//
// Nodes are mutated exclusively from the UI's Bubble Tea Update loop (a
// single goroutine) as results arrive as messages — nothing here does
// I/O or touches a worker pool, so it needs no locking.
package sidetree

import (
	"sort"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
)

// Node is one entry of one tree, at RelPath below that tree's root.
type Node struct {
	Name    string
	Type    diffmodel.EntryType
	RelPath string
	Parent  *Node

	// HaveStat reports whether a stat result has landed for this entry —
	// including a failed one, which is what StatErr records. A directory
	// is never statted (it contributes a count to the totals, not a
	// size), so this only ever becomes true for a file or symlink, and
	// Size/Mtime/LinkTarget mean anything only while StatErr is nil.
	HaveStat   bool
	StatErr    error
	Size       int64
	Mtime      time.Time
	LinkTarget string // symlinks only (SPEC.md §7)

	// Directory-only fields.
	Listed   bool
	Listing  bool
	ListErr  error
	Children []*Node

	// PendingListing and PendingStat count listing and metadata jobs
	// queued or in flight anywhere in this node's subtree, including
	// itself. Package session keeps them in sync via AdjustPending* as
	// jobs are enqueued and results applied, so a directory can tell
	// whether this side of it is still being discovered or measured.
	// Both are per side because both kinds of job read one side alone —
	// unlike a content comparison, which is a statement about a pair.
	PendingListing int
	PendingStat    int

	// totals is what this node's descendants hold, kept incrementally by
	// one upward walk per mutation rather than a subtree walk per render
	// (see Totals).
	totals Totals
}

// Totals aggregates what one subtree holds, for the details panel
// (SPEC.md §4.2). Entry counts come from listing, which already knows
// every entry's type; Size comes from stat results, so Files is an upper
// bound on SizedFiles until the subtree has been statted through, and
// Size is a lower bound on the real size until then.
type Totals struct {
	Dirs     int
	Files    int
	Symlinks int

	Size       int64
	SizedFiles int
}

func (t *Totals) add(o Totals) {
	t.Dirs += o.Dirs
	t.Files += o.Files
	t.Symlinks += o.Symlinks
	t.Size += o.Size
	t.SizedFiles += o.SizedFiles
}

func (t Totals) sub(o Totals) Totals {
	t.Dirs -= o.Dirs
	t.Files -= o.Files
	t.Symlinks -= o.Symlinks
	t.Size -= o.Size
	t.SizedFiles -= o.SizedFiles
	return t
}

func (t Totals) isZero() bool { return t == Totals{} }

// Tree is one side's whole tree plus the RelPath index into it. Within
// one real filesystem directory a name identifies exactly one entry, so
// unlike a merged tree — where a file on one side and a directory on the
// other share a path (SPEC.md §3.1) — this index needs no per-type
// mirror to stay unambiguous.
type Tree struct {
	Side  diffmodel.Side
	Root  *Node
	Index map[string]*Node
}

// NewTree creates a tree holding just its root, which always exists
// (main.go validates both roots before the TUI starts).
func NewTree(side diffmodel.Side) *Tree {
	root := &Node{Type: diffmodel.Dir}
	return &Tree{Side: side, Root: root, Index: map[string]*Node{"": root}}
}

func (n *Node) IsDir() bool { return n.Type == diffmodel.Dir }

// Totals reports what n's descendants, at any depth, hold — n itself is
// never counted, so a directory row reports what's inside it, and a
// tree root's totals are that whole side (SPEC.md §4.3.1).
//
// It only covers what's been discovered so far: counts grow as listing
// proceeds, and SizedFiles/Size grow as metadata arrives.
func (n *Node) Totals() Totals { return n.totals }

// ChildRelPath computes the RelPath of a child named name under parent.
func ChildRelPath(parent *Node, name string) string {
	if parent.RelPath == "" {
		return name
	}
	return parent.RelPath + "/" + name
}

// ApplyListing records the result of listing n (a directory) and builds
// its Children, indexing them. It enqueues no follow-up work — that
// requires the worker queues, which live in package session.
func (t *Tree) ApplyListing(n *Node, entries []diffmodel.ListedEntry, listErr error) {
	n.Listed = true
	n.Listing = false
	n.ListErr = listErr

	var delta Totals
	for _, old := range n.Children {
		delta = delta.sub(contribution(old))
		delete(t.Index, old.RelPath)
	}

	n.Children = make([]*Node, 0, len(entries))
	for _, e := range entries {
		c := &Node{Name: e.Name, Type: e.Type, RelPath: ChildRelPath(n, e.Name)}
		delta.add(linkChild(n, c))
		t.Index[c.RelPath] = c
	}
	sortChildren(n.Children)
	adjustTotalsUpward(n, delta)
}

func sortChildren(children []*Node) {
	sort.Slice(children, func(i, j int) bool {
		return diffmodel.EntryLess(children[i].Name, children[i].Type, children[j].Name, children[j].Type)
	})
}

// Stat is the metadata a stat job read for one entry.
type Stat struct {
	Size       int64
	Mtime      time.Time
	LinkTarget string
	Err        error
}

// ApplyStat records metadata read for n. A directory is never statted
// and a symlink's size is never counted (comparing one reads its target
// string, not the file it names, SPEC.md §7), so only a file ever
// contributes a size to the totals — but the result is recorded either
// way, since the details panel shows an mtime for both and a symlink is
// compared by the target this read.
func ApplyStat(n *Node, s Stat) {
	before := ownContribution(n)
	n.HaveStat = true
	n.StatErr = s.Err
	n.Size, n.Mtime, n.LinkTarget = s.Size, s.Mtime, s.LinkTarget
	// The ancestors' totals count descendants only, so the delta starts
	// at the parent.
	adjustTotalsUpward(n.Parent, ownContribution(n).sub(before))
}

// AdjustPendingListing changes n's PendingListing count by delta and
// propagates the same change up through Parent to the root.
func AdjustPendingListing(n *Node, delta int) {
	for cur := n; cur != nil; cur = cur.Parent {
		cur.PendingListing += delta
	}
}

// AdjustPendingStat is AdjustPendingListing for metadata jobs.
func AdjustPendingStat(n *Node, delta int) {
	for cur := n; cur != nil; cur = cur.Parent {
		cur.PendingStat += delta
	}
}

// AddChild links child — with whatever subtree it already carries —
// into parent's Children, and folds its totals into parent and every
// ancestor. Assigning to Children directly leaves those totals stale, so
// this is the only supported way to attach a node.
func AddChild(parent, child *Node) {
	adjustTotalsUpward(parent, linkChild(parent, child))
}

// linkChild is AddChild without the upward walk: it returns the delta
// the ancestors still need, so a caller attaching many children at once
// (ApplyListing) can sum them and walk up a single time.
func linkChild(parent, child *Node) Totals {
	child.Parent = parent
	parent.Children = append(parent.Children, child)
	return contribution(child)
}

// contribution is what child accounts for in its ancestors' totals:
// everything its own subtree already carries, plus what child itself
// counts for.
func contribution(child *Node) Totals {
	d := child.totals
	d.add(ownContribution(child))
	return d
}

// ownContribution is what a single node counts for, ignoring anything
// below it: one entry of its own type, carrying its size once that has
// actually been read.
func ownContribution(n *Node) Totals {
	var t Totals
	switch n.Type {
	case diffmodel.Dir:
		t.Dirs = 1
	case diffmodel.Symlink:
		t.Symlinks = 1
	default:
		t.Files = 1
	}
	if n.HaveStat && n.StatErr == nil && n.Type == diffmodel.File {
		t.Size, t.SizedFiles = n.Size, 1
	}
	return t
}

// adjustTotalsUpward applies delta to n and every ancestor of n. Cost is
// the depth of the tree, not the size of the subtree.
func adjustTotalsUpward(n *Node, delta Totals) {
	if delta.isZero() {
		return
	}
	for cur := n; cur != nil; cur = cur.Parent {
		cur.totals.add(delta)
	}
}
