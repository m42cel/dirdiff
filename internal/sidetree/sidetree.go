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
	"sync/atomic"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
)

// Node is one entry of one tree, at RelPath below that tree's root.
type Node struct {
	Name    string
	Type    diffmodel.EntryType
	RelPath string
	Parent  *Node

	// Gen identifies this node as it currently stands: unique when the
	// node is created, and renewed whenever a refresh discards what was
	// read about it. A job carries the Gen of the node it was created
	// for, so a result that lands after its node was removed or re-read
	// is recognisably stale even though its RelPath still resolves.
	Gen uint64

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
	root := &Node{Type: diffmodel.Dir, Gen: newGen()}
	return &Tree{Side: side, Root: root, Index: map[string]*Node{"": root}}
}

// lastGen hands out Node.Gen values. It's shared by every tree, and
// atomic only because tests run several sessions in parallel; within one
// session every mutation happens on the UI's goroutine.
var lastGen atomic.Uint64

func newGen() uint64 { return lastGen.Add(1) }

func (n *Node) IsDir() bool { return n.Type == diffmodel.Dir }

// Contains reports whether n is still part of t — false once a refresh
// has removed it, or an ancestor of it.
func (t *Tree) Contains(n *Node) bool { return t.Index[n.RelPath] == n }

// Walk calls fn for n and every node below it, parents first.
func Walk(n *Node, fn func(*Node)) {
	fn(n)
	for _, c := range n.Children {
		Walk(c, fn)
	}
}

// Totals reports what n's descendants, at any depth, hold — n itself is
// never counted, so a directory row reports what's inside it, and a
// tree root's totals are that whole side (SPEC.md §4.3.1).
//
// It only covers what's been discovered so far: counts grow as listing
// proceeds, and SizedFiles/Size grow as metadata arrives.
func (n *Node) Totals() Totals { return n.totals }

// childRelPath computes the RelPath of a child named name under parent.
func childRelPath(parent *Node, name string) string {
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
		c := &Node{Name: e.Name, Type: e.Type, RelPath: childRelPath(n, e.Name), Gen: newGen()}
		delta.add(linkChild(n, c))
		t.Index[c.RelPath] = c
	}
	sortChildren(n.Children)
	adjustTotalsUpward(n, delta)
}

// Refresh applies a fresh listing of n, an already-listed directory, to
// the children inScope selects, and leaves every other child exactly as
// it was (SPEC.md §5.5). Matching is by (name, type), as everywhere
// (SPEC.md §3.1):
//
//   - removed: an in-scope child the listing no longer has. It's
//     unlinked with its whole subtree and dropped from the index, and
//     whatever jobs were counted for it are subtracted from the
//     ancestors' pending counts, so a result still in flight for it must
//     be discarded on arrival rather than applied (see Gen).
//   - kept: an in-scope child the listing still has. It stays the same
//     node, so every pairing showing it keeps its row, but a file or
//     symlink forgets its metadata and gets a new Gen. A directory keeps
//     its children here: they are only as stale as the directory's own
//     listing, which the caller re-reads next.
//   - added: an in-scope entry with no child yet, created unlisted and
//     unstatted, the same as ApplyListing creates them.
func (t *Tree) Refresh(n *Node, entries []diffmodel.ListedEntry, listErr error, inScope func(name string) bool) (removed, kept, added []*Node) {
	n.Listing = false
	n.ListErr = listErr

	type key struct {
		name string
		typ  diffmodel.EntryType
	}
	fresh := map[key]bool{}
	for _, e := range entries {
		if inScope(e.Name) {
			fresh[key{e.Name, e.Type}] = true
		}
	}

	var delta Totals
	have := map[key]bool{}
	children := n.Children[:0:0]
	for _, c := range n.Children {
		k := key{c.Name, c.Type}
		switch {
		case !inScope(c.Name):
			children = append(children, c)
		case fresh[k]:
			delta.add(forget(c))
			have[k] = true
			kept = append(kept, c)
			children = append(children, c)
		default:
			delta = delta.sub(contribution(c))
			AdjustPendingListing(n, -c.PendingListing)
			AdjustPendingStat(n, -c.PendingStat)
			t.unindex(c)
			removed = append(removed, c)
		}
	}
	n.Children = children

	for _, e := range entries {
		if !inScope(e.Name) || have[key{e.Name, e.Type}] {
			continue
		}
		c := &Node{Name: e.Name, Type: e.Type, RelPath: childRelPath(n, e.Name), Gen: newGen()}
		delta.add(linkChild(n, c))
		t.Index[c.RelPath] = c
		added = append(added, c)
	}
	sortChildren(n.Children)
	adjustTotalsUpward(n, delta)
	return removed, kept, added
}

// forget discards the metadata read for a kept file or symlink and
// returns the change in what it counts for in its ancestors' totals,
// which the caller applies from the parent up. Its own outstanding stat
// is uncounted the same way a removed node's is, and its new Gen makes
// that stat's result stale. A directory has no metadata of its own.
func forget(n *Node) Totals {
	if n.IsDir() {
		return Totals{}
	}
	before := ownContribution(n)
	n.HaveStat, n.StatErr = false, nil
	n.Size, n.Mtime, n.LinkTarget = 0, time.Time{}, ""
	AdjustPendingStat(n, -n.PendingStat)
	n.Gen = newGen()
	return ownContribution(n).sub(before)
}

// unindex drops n and everything below it from the index.
func (t *Tree) unindex(n *Node) {
	Walk(n, func(d *Node) {
		if t.Index[d.RelPath] == d {
			delete(t.Index, d.RelPath)
		}
	})
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

// linkChild attaches child to parent and returns the delta the
// ancestors still need, so ApplyListing can sum every child's and walk
// up a single time. It's the only way a node is linked in — assigning to
// Children directly would leave the totals stale.
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
