package pairtree

import (
	"testing"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/sidetree"
)

// refresh re-lists dir's sides with entries, scoped to every name, and
// brings the pairing back in line — what package session does once a
// refresh's listings have landed on both sides.
func (f *fixture) refresh(dir *Node, entries ...entry) (removed, reset, added []*Node) {
	kept := map[*sidetree.Node]bool{}
	for _, sd := range diffmodel.Sides {
		sn := sideOf(dir, sd)
		if sn == nil {
			continue
		}
		var listed []diffmodel.ListedEntry
		for _, e := range entries {
			if e.presence != missingFrom(sd) {
				listed = append(listed, diffmodel.ListedEntry{Name: e.name, Type: e.typ})
			}
		}
		_, k, _ := f.trees[sd].Refresh(sn, listed, nil, func(string) bool { return true })
		for _, n := range k {
			kept[n] = true
		}
	}
	return f.pairing.Refresh(dir, func(sn *sidetree.Node) bool { return kept[sn] })
}

func TestRefreshResetsAKeptRowInPlace(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothF("f.txt"))
	row := f.find("f.txt")
	f.statBoth("f.txt", sidetree.Stat{Size: 1, Mtime: time.Unix(1, 0)}, sidetree.Stat{Size: 2, Mtime: time.Unix(1, 0)})
	if row.Result != diffmodel.Differs {
		t.Fatalf("setup: result = %v; want Differs", row.Result)
	}
	gen := row.Gen

	removed, reset, added := f.refresh(f.root, bothF("f.txt"))

	if len(removed) != 0 || len(added) != 0 || len(reset) != 1 || reset[0] != row {
		t.Fatalf("removed %d, reset %v, added %d; want the one row reset in place", len(removed), reset, len(added))
	}
	if row.Result != diffmodel.Unknown || row.Level != diffmodel.NotCompared || row.Gen == gen {
		t.Fatalf("after reset: result %v, level %v, gen renewed %v; want not compared with a new Gen", row.Result, row.Level, row.Gen != gen)
	}
	if !f.pairing.Contains(row) {
		t.Fatal("a reset row must stay in the pairing")
	}
	assertTallies(t, f.root)
}

// A row's presence is fixed at creation (SPEC.md §3), so a counterpart
// appearing, or one side disappearing, replaces the row instead.
func TestRefreshReplacesARowWhosePresenceChanged(t *testing.T) {
	f := newFixture()
	f.list(f.root, leftF("appears.txt"), bothF("vanishes.txt"), bothD("gone"))
	appears, vanishes, gone := f.find("appears.txt"), f.find("vanishes.txt"), f.find("gone")
	f.list(gone, bothF("deep.txt"))

	removed, _, added := f.refresh(f.root, bothF("appears.txt"), leftF("vanishes.txt"))

	if len(removed) != 3 {
		t.Fatalf("removed %d rows; want all three", len(removed))
	}
	for _, n := range []*Node{appears, vanishes, gone} {
		if f.pairing.Contains(n) {
			t.Errorf("row %q still in the pairing", n.PairRel)
		}
	}
	if _, ok := f.pairing.Row("gone/deep.txt"); ok {
		t.Error("a removed directory's descendants must leave the index too")
	}
	if len(added) != 2 {
		t.Fatalf("added %d rows; want replacements for the two files", len(added))
	}
	if n := f.find("appears.txt"); n.Presence() != diffmodel.Both {
		t.Errorf("appears.txt presence = %v; want Both", n.Presence())
	}
	if n := f.find("vanishes.txt"); n.Presence() != diffmodel.LeftOnly {
		t.Errorf("vanishes.txt presence = %v; want LeftOnly", n.Presence())
	}
	assertTallies(t, f.root)
}

func TestRefreshUncountsARemovedRowsPendingJobs(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothD("sub"), bothF("kept.txt"))
	sub := f.find("sub")
	f.list(sub, bothF("f.txt"))
	AdjustPendingCompare(f.find("sub/f.txt"), 1)
	AdjustPendingCompare(f.find("kept.txt"), 1)

	f.refresh(f.root, bothF("kept.txt"))

	if f.root.PendingCompare != 0 {
		t.Fatalf("root PendingCompare = %d; want 0 — the removed row's job and the reset row's are both uncounted", f.root.PendingCompare)
	}
}

func TestRefreshRollsUpWhatRemains(t *testing.T) {
	f := newFixture()
	f.list(f.root, leftF("only-left.txt"), bothF("same.txt"))
	f.statBoth("same.txt", sidetree.Stat{Size: 1, Mtime: time.Unix(1, 0)}, sidetree.Stat{Size: 1, Mtime: time.Unix(1, 0)})
	if f.root.Result != diffmodel.Differs {
		t.Fatalf("setup: root = %v; want Differs from the one-sided file", f.root.Result)
	}

	// The one-sided file is gone; same.txt is kept, so it's back to
	// unknown until it's measured again.
	f.refresh(f.root, bothF("same.txt"))

	if f.root.Result != diffmodel.Unknown {
		t.Fatalf("root = %v; want Unknown — nothing differs any more, but same.txt hasn't been re-read", f.root.Result)
	}
}
