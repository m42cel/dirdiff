package pairtree

import (
	"strings"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/sidetree"
)

// fixture is a pairing over two side trees, driven the way package
// session drives one: listings go into a side tree and the rows fall out
// of a merge, rather than being hand-built. That keeps these tests
// honest about the one thing the merge has to get right — which entries
// pair up and which stay one-sided.
type fixture struct {
	trees [2]*sidetree.Tree
	root  *Node
}

func newFixture() *fixture {
	f := &fixture{trees: [2]*sidetree.Tree{
		diffmodel.Left:  sidetree.NewTree(diffmodel.Left),
		diffmodel.Right: sidetree.NewTree(diffmodel.Right),
	}}
	f.root = NewRoot(f.trees[diffmodel.Left].Root, f.trees[diffmodel.Right].Root)
	return f
}

// entry is one child a test asks a directory to contain, and which
// side(s) it exists on.
type entry struct {
	name     string
	typ      diffmodel.EntryType
	presence diffmodel.Presence
}

func bothF(name string) entry  { return entry{name, diffmodel.File, diffmodel.Both} }
func bothD(name string) entry  { return entry{name, diffmodel.Dir, diffmodel.Both} }
func bothL(name string) entry  { return entry{name, diffmodel.Symlink, diffmodel.Both} }
func leftF(name string) entry  { return entry{name, diffmodel.File, diffmodel.LeftOnly} }
func leftD(name string) entry  { return entry{name, diffmodel.Dir, diffmodel.LeftOnly} }
func rightF(name string) entry { return entry{name, diffmodel.File, diffmodel.RightOnly} }
func rightD(name string) entry { return entry{name, diffmodel.Dir, diffmodel.RightOnly} }

// list gives dir the listed entries on each side it exists on, then
// merges — one listing per side, as the two listing jobs would deliver
// them, with the merge firing once both have landed.
func (f *fixture) list(dir *Node, entries ...entry) {
	f.listErr(dir, nil, entries...)
}

// listErr is list with a listing failure on the left side, for the
// rollup's error case.
func (f *fixture) listErr(dir *Node, leftErr error, entries ...entry) {
	for _, sd := range diffmodel.Sides {
		sn := sideOf(dir, sd)
		if sn == nil {
			continue
		}
		var listed []diffmodel.ListedEntry
		for _, e := range entries {
			if e.presence == missingFrom(sd) {
				continue
			}
			listed = append(listed, diffmodel.ListedEntry{Name: e.name, Type: e.typ})
		}
		err := error(nil)
		if sd == diffmodel.Left {
			err = leftErr
		}
		f.trees[sd].ApplyListing(sn, listed, err)
	}
	f.merge(dir)
}

// listSide lists dir on one side only, leaving the other side's listing
// outstanding — the state the merge gating exists for.
func (f *fixture) listSide(dir *Node, sd diffmodel.Side, entries ...entry) {
	var listed []diffmodel.ListedEntry
	for _, e := range entries {
		if e.presence == missingFrom(sd) {
			continue
		}
		listed = append(listed, diffmodel.ListedEntry{Name: e.name, Type: e.typ})
	}
	f.trees[sd].ApplyListing(sideOf(dir, sd), listed, nil)
	f.merge(dir)
}

// merge is package session's own recursive merge: whatever rows the two
// sides now allow, plus any directory row created after its own subtree
// was already listed.
func (f *fixture) merge(p *Node) {
	for _, c := range Merge(p) {
		if c.IsDir() {
			f.merge(c)
		}
	}
}

// find returns the row at relPath, or nil. A file and a directory of the
// same name are two rows sharing one path (SPEC.md §3.1); find answers
// with whichever sorts first, so tests about that case walk Children
// themselves.
func (f *fixture) find(relPath string) *Node {
	var walk func(n *Node) *Node
	walk = func(n *Node) *Node {
		if n.RelPath == relPath {
			return n
		}
		for _, c := range n.Children {
			if relPath == c.RelPath || strings.HasPrefix(relPath, c.RelPath+"/") {
				if got := walk(c); got != nil {
					return got
				}
			}
		}
		return nil
	}
	return walk(f.root)
}

// stat feeds one side's metadata for the row at relPath and applies
// whatever verdict that makes possible, the way session does on a stat
// result.
func (f *fixture) stat(relPath string, sd diffmodel.Side, s sidetree.Stat) {
	sidetree.ApplyStat(f.trees[sd].Index[relPath], s)
	if p := f.find(relPath); p != nil {
		ApplyMetadata(p)
	}
}

// statBoth is stat on both sides at once, for a row whose two sides are
// measured before anything asks about the verdict.
func (f *fixture) statBoth(relPath string, left, right sidetree.Stat) {
	sidetree.ApplyStat(f.trees[diffmodel.Left].Index[relPath], left)
	sidetree.ApplyStat(f.trees[diffmodel.Right].Index[relPath], right)
	if p := f.find(relPath); p != nil {
		ApplyMetadata(p)
	}
}

func sideOf(p *Node, sd diffmodel.Side) *sidetree.Node {
	if sd == diffmodel.Right {
		return p.Right
	}
	return p.Left
}

// missingFrom is the presence that means "not on this side".
func missingFrom(sd diffmodel.Side) diffmodel.Presence {
	if sd == diffmodel.Right {
		return diffmodel.LeftOnly
	}
	return diffmodel.RightOnly
}
