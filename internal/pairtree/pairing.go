package pairtree

import (
	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/sidetree"
)

// Pairing is two directories matched against each other, entry by entry:
// normally the two compared roots, but under a sub-compare (SPEC.md
// §4.9) any two directories at all, at unrelated paths. It owns the
// merged tree over them and the indexes into it.
//
// The two sides themselves are not owned: they're sidetree nodes, shared
// with every other pairing covering them. So opening a pairing over
// already-scanned subtrees costs no I/O — all it builds is the matching.
// What a pairing does own is its content verdicts, which are statements
// about a pair of files and mean nothing to any other pairing (comparing
// A with B says nothing about A vs C).
type Pairing struct {
	Root *Node

	// rowFor maps a side node to the row pairing it, per side, so a
	// listing or metadata result can be fanned out from the side tree it
	// landed in to the rows showing it in O(1). A side node appears in at
	// most one row of a given pairing.
	rowFor [2]map[*sidetree.Node]*Node

	// rows indexes both-sided rows by pair-relative path, for routing a
	// content comparison's result back. Only both-sided rows are indexed,
	// which is what makes the path unambiguous: matching is by (name,
	// type), so two both-sided rows can never share a path, and a row
	// sharing a path with another is by construction one-sided (SPEC.md
	// §3.1) and has nothing to compare.
	rows map[string]*Node
}

// NewPairing creates a pairing over two directories and builds whatever
// rows their already-known contents allow.
func NewPairing(left, right *sidetree.Node) *Pairing {
	p := &Pairing{
		Root: &Node{Left: left, Right: right, Type: diffmodel.Dir, Gen: newGen()},
		rowFor: [2]map[*sidetree.Node]*Node{
			diffmodel.Left:  {},
			diffmodel.Right: {},
		},
		rows: map[string]*Node{},
	}
	p.index(p.Root)
	p.Merge(p.Root)
	return p
}

// Merge builds whatever rows n's two sides now allow and indexes them,
// descending into any new directory row whose own two sides happen to be
// listed already — a row created after its subtree was scanned has no
// listing result left to arrive, which is the normal case for a pairing
// opened over a subtree the background scan already covered. It returns
// the rows it added directly under n.
func (p *Pairing) Merge(n *Node) []*Node {
	added := Merge(n)
	for _, c := range added {
		p.index(c)
		if c.IsDir() {
			p.Merge(c)
		}
	}
	return added
}

// Refresh brings n's rows back in line with its sides after a refresh
// has changed them (SPEC.md §5.5). kept reports whether a side node was
// kept by that refresh, i.e. re-read rather than created or removed.
//
//   - removed: a row whose sides are no longer what n's sides hold under
//     its (name, type) — one of them was removed, or a counterpart has
//     appeared on a side it was missing from. A row's presence never
//     changes (SPEC.md §3), so it's dropped with its subtree, and the
//     Merge below builds its replacement from whatever sides remain.
//   - reset: a row standing over a kept side node, which keeps its place
//     but goes back to not compared (see Reset).
//   - added: what that Merge built.
func (p *Pairing) Refresh(n *Node, kept func(*sidetree.Node) bool) (removed, reset, added []*Node) {
	var current [2]map[childKey]*sidetree.Node
	for _, sd := range diffmodel.Sides {
		sn := n.Side(sd)
		if sn == nil {
			continue
		}
		current[sd] = make(map[childKey]*sidetree.Node, len(sn.Children))
		for _, c := range sn.Children {
			current[sd][childKey{c.Name, c.Type}] = c
		}
	}

	for _, c := range append([]*Node(nil), n.Children...) {
		k := childKey{c.Name(), c.Type}
		stale := false
		for _, sd := range diffmodel.Sides {
			if c.Side(sd) != current[sd][k] {
				stale = true
			}
		}
		switch {
		case stale:
			p.remove(c)
			removed = append(removed, c)
		case (c.Left != nil && kept(c.Left)) || (c.Right != nil && kept(c.Right)):
			Reset(c)
			reset = append(reset, c)
		}
	}
	return removed, reset, p.Merge(n)
}

// remove unlinks n and its subtree from the tree and from the pairing's
// indexes, taking its statuses out of the ancestors' tallies and its
// outstanding content jobs out of their pending counts. Results still in
// flight for it then find no row to land in (or a newer one, with a
// different Gen) and are dropped.
func (p *Pairing) remove(n *Node) {
	parent := n.Parent
	for i, c := range parent.Children {
		if c == n {
			parent.Children = append(parent.Children[:i], parent.Children[i+1:]...)
			break
		}
	}
	adjustTalliesUpward(parent, contribution(n).negated())
	AdjustPendingCompare(parent, -n.PendingCompare)
	Walk(n, func(d *Node) {
		for _, sd := range diffmodel.Sides {
			if sn := d.Side(sd); sn != nil && p.rowFor[sd][sn] == d {
				delete(p.rowFor[sd], sn)
			}
		}
		if p.rows[d.PairRel] == d {
			delete(p.rows, d.PairRel)
		}
	})
	recomputeResultUpward(parent)
}

// Contains reports whether n is still one of this pairing's rows — false
// once a refresh has removed it, or an ancestor of it.
func (p *Pairing) Contains(n *Node) bool {
	for _, sd := range diffmodel.Sides {
		if sn := n.Side(sd); sn != nil {
			return p.rowFor[sd][sn] == n
		}
	}
	return false
}

// Walk calls fn for n and every row below it, parents first.
func Walk(n *Node, fn func(*Node)) {
	fn(n)
	for _, c := range n.Children {
		Walk(c, fn)
	}
}

func (p *Pairing) index(n *Node) {
	for _, sd := range diffmodel.Sides {
		if sn := n.Side(sd); sn != nil {
			p.rowFor[sd][sn] = n
		}
	}
	if n.Presence() == diffmodel.Both && n != p.Root {
		p.rows[n.PairRel] = n
	}
}

// RowFor returns the row one side node appears as in this pairing, if
// the pairing covers it at all.
func (p *Pairing) RowFor(sd diffmodel.Side, sn *sidetree.Node) (*Node, bool) {
	n, ok := p.rowFor[sd][sn]
	return n, ok
}

// Row returns the both-sided row at pairRel — the only kind a content
// comparison can target, and therefore the only kind its result has to
// be routed back to.
func (p *Pairing) Row(pairRel string) (*Node, bool) {
	n, ok := p.rows[pairRel]
	return n, ok
}

// Side returns n's node on one side, or nil if n doesn't exist there.
func (n *Node) Side(sd diffmodel.Side) *sidetree.Node {
	if sd == diffmodel.Right {
		return n.Right
	}
	return n.Left
}
