package session

import (
	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/pairtree"
	"github.com/m42cel/dirdiff/internal/scan"
	"github.com/m42cel/dirdiff/internal/sidetree"
)

// refreshPart is one directory a refresh re-lists on one side, the
// entries of it the refresh is about, and the listing once it lands.
type refreshPart struct {
	side   diffmodel.Side
	node   *sidetree.Node
	all    bool
	names  map[string]bool
	result *scan.ListResult
}

func (p *refreshPart) inScope(name string) bool { return p.all || p.names[name] }

// widen extends p's scope by o's. Widening after p's listing has landed
// is fine: a listing holds the whole directory, whatever it was asked
// for.
func (p *refreshPart) widen(o *refreshPart) {
	if o.all {
		p.all = true
		return
	}
	if p.names == nil {
		p.names = map[string]bool{}
	}
	for name := range o.names {
		p.names[name] = true
	}
}

// refresh is a set of directory listings applied together: the two
// sides of one row's parent, say. Its listings are held until all of
// them have landed, so a row never shows one side re-read and the other
// not (SPEC.md §5.5's "old rows stay until the new listing lands").
type refresh struct {
	parts []*refreshPart
}

func (r *refresh) part(sd diffmodel.Side, n *sidetree.Node) *refreshPart {
	for _, p := range r.parts {
		if p.side == sd && p.node == n {
			return p
		}
	}
	return nil
}

func (r *refresh) ready() bool {
	for _, p := range r.parts {
		if p.result == nil {
			return false
		}
	}
	return true
}

// Refresh re-reads target from disk (SPEC.md §5.5). A row is refreshed
// by re-listing its parent on both sides — the only way to learn that
// it's gone, changed type, or appeared on a side it was missing from —
// and applying that listing to the entries of its name alone; a
// directory's subtree is then re-read level by level below it. A
// pairing's root has no parent within the pairing, so it is re-listed in
// place instead, all of its entries in scope. Refreshing the current
// directory is therefore just Refresh on its own row.
func (s *Session) Refresh(id PairingID, target *pairtree.Node) {
	p, ok := s.pairings[id]
	if !ok || target == nil {
		return
	}
	var parts []*refreshPart
	if target == p.Root {
		for _, sd := range diffmodel.Sides {
			parts = append(parts, &refreshPart{side: sd, node: target.Side(sd), all: true})
		}
	} else {
		for _, sd := range diffmodel.Sides {
			if sn := target.Parent.Side(sd); sn != nil {
				parts = append(parts, &refreshPart{side: sd, node: sn, names: map[string]bool{target.Name(): true}})
			}
		}
	}
	s.startRefresh(parts)
}

// Refreshes counts the refreshes applied so far. It lets the UI tell
// that a batch of results replaced rows, which is when the cursor has to
// look for its row again rather than keep its index.
func (s *Session) Refreshes() uint64 { return s.refreshes }

// startRefresh enqueues the listings parts need and records them as one
// refresh. A directory not listed yet is skipped: its first listing is
// still to come, and will be as fresh as a refresh's. A directory
// already being re-listed by another refresh joins that refresh into
// this one, widening its scope rather than reading it twice.
func (s *Session) startRefresh(parts []*refreshPart) {
	r := &refresh{}
	for _, p := range parts {
		if p.node == nil || !p.node.Listed {
			continue
		}
		if old := s.refreshing[p.side][p.node]; old != nil && old != r {
			s.absorb(r, old)
		}
		if existing := r.part(p.side, p.node); existing != nil {
			existing.widen(p)
			continue
		}
		r.parts = append(r.parts, p)
		s.refreshing[p.side][p.node] = r
		s.enqueueList(p.side, p.node)
	}
	if len(r.parts) > 0 && r.ready() {
		s.applyRefresh(r)
	}
}

// absorb moves every part of old into r.
func (s *Session) absorb(r, old *refresh) {
	for _, op := range old.parts {
		if existing := r.part(op.side, op.node); existing != nil {
			existing.widen(op)
			if existing.result == nil {
				existing.result = op.result
			}
		} else {
			r.parts = append(r.parts, op)
		}
		s.refreshing[op.side][op.node] = r
	}
}

// dropPart removes a part whose directory a refresh has removed, and
// reports whether that leaves rf with only landed listings — in which
// case nothing else will ever trigger applying it.
func (s *Session) dropPart(rf *refresh, sd diffmodel.Side, n *sidetree.Node) bool {
	delete(s.refreshing[sd], n)
	for i, p := range rf.parts {
		if p.side == sd && p.node == n {
			rf.parts = append(rf.parts[:i], rf.parts[i+1:]...)
			break
		}
	}
	return len(rf.parts) > 0 && rf.ready()
}

// applyRefresh applies a refresh whose listings have all landed: first
// to the side trees, then to every open pairing covering the re-listed
// directories, and finally re-lists each kept subdirectory the same way,
// which is how a refresh reaches the whole subtree below its target.
//
// Whatever is new or reset starts from --level and nothing else (SPEC.md
// §5.5): a deeper comparison asked for earlier, on it or on an ancestor,
// is not carried over.
func (s *Session) applyRefresh(rf *refresh) {
	for _, p := range rf.parts {
		delete(s.refreshing[p.side], p.node)
	}

	kept := map[*sidetree.Node]bool{}
	var applied, nested []*refreshPart
	var follow []*refresh
	for _, p := range rf.parts {
		t := s.sides[p.side].tree
		if !t.Contains(p.node) {
			continue // removed by another part of this same refresh
		}
		removed, keptNodes, added := t.Refresh(p.node, p.result.Entries, p.result.Err, p.inScope)

		// The tree has already uncounted these jobs; what's left is to
		// take them out of the queues, where a merge into a stale queued
		// job would otherwise swallow the fresh one.
		var keys []string
		for _, r := range removed {
			sidetree.Walk(r, func(d *sidetree.Node) {
				keys = append(keys, sideKey(p.side, d.RelPath))
				if other := s.refreshing[p.side][d]; other != nil && s.dropPart(other, p.side, d) {
					follow = append(follow, other)
				}
			})
		}
		s.listQ.Remove(keys...)
		for _, k := range keptNodes {
			kept[k] = true
			switch {
			case !k.IsDir():
				keys = append(keys, sideKey(p.side, k.RelPath))
			case k.Listed:
				nested = append(nested, &refreshPart{side: p.side, node: k, all: true})
			}
		}
		s.examineQ.Remove(keys...)

		for _, a := range added {
			if a.IsDir() {
				s.enqueueList(p.side, a)
			}
		}
		applied = append(applied, p)
	}

	isKept := func(sn *sidetree.Node) bool { return kept[sn] }
	for id, pr := range s.pairings {
		seen := map[*pairtree.Node]bool{}
		for _, p := range applied {
			if !s.sides[p.side].tree.Contains(p.node) {
				continue
			}
			row, ok := pr.RowFor(p.side, p.node)
			if !ok || seen[row] {
				continue
			}
			seen[row] = true
			s.refreshRows(id, pr, row, isKept)
		}
	}
	s.refreshes++

	// Kept subdirectories of the same name are re-listed as one refresh,
	// so the two sides of a row below the target also swap together.
	byName := map[string][]*refreshPart{}
	var names []string
	for _, p := range nested {
		if _, ok := byName[p.node.Name]; !ok {
			names = append(names, p.node.Name)
		}
		byName[p.node.Name] = append(byName[p.node.Name], p)
	}
	for _, name := range names {
		s.startRefresh(byName[name])
	}
	for _, f := range follow {
		if len(f.parts) > 0 && f.ready() {
			s.applyRefresh(f)
		}
	}
}

// refreshRows brings one pairing's rows under row back in line with its
// refreshed sides, retires the content jobs of every row it removed or
// reset, and arms what's new or reset at --level.
func (s *Session) refreshRows(id PairingID, p *pairtree.Pairing, row *pairtree.Node, kept func(*sidetree.Node) bool) {
	removed, reset, added := p.Refresh(row, kept)

	// Only both-sided files have content jobs. Keying the removal by
	// those alone matters: a one-sided row can share its path with an
	// unrelated both-sided one of the other type (SPEC.md §3.1).
	var keys []string
	for _, r := range removed {
		pairtree.Walk(r, func(d *pairtree.Node) {
			if !d.IsDir() && d.Presence() == diffmodel.Both {
				keys = append(keys, pairKey(id, d.PairRel))
			}
		})
	}
	for _, r := range reset {
		if !r.IsDir() && r.Presence() == diffmodel.Both {
			keys = append(keys, pairKey(id, r.PairRel))
		}
	}
	s.examineQ.Remove(keys...)

	if s.autoLevel == diffmodel.NotCompared {
		return
	}
	for _, r := range reset {
		if r.IsDir() {
			// Its children are reset or replaced when its own re-listing
			// lands; this is what a child discovered later inherits.
			r.PendingRecursiveLevel = s.autoLevel
			continue
		}
		s.examine(id, r, s.autoLevel)
	}
	s.armRecursive(id, added, s.autoLevel)
}
