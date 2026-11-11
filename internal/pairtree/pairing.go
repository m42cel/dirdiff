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
		Root: &Node{Left: left, Right: right, Type: diffmodel.Dir},
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
// opened over a subtree the background scan already covered.
func (p *Pairing) Merge(n *Node) {
	for _, c := range Merge(n) {
		p.index(c)
		if c.IsDir() {
			p.Merge(c)
		}
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
