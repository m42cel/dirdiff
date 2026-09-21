package sidetree

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
)

// totalsByWalk is the definition Node.totals has to agree with at all
// times: visit every descendant and add up what each one counts for.
func totalsByWalk(n *Node) Totals {
	var out Totals
	for _, c := range n.Children {
		out.add(ownContribution(c))
		out.add(totalsByWalk(c))
	}
	return out
}

func assertTotals(t *testing.T, n *Node) {
	t.Helper()
	if got, want := n.Totals(), totalsByWalk(n); got != want {
		t.Fatalf("node %q: totals = %+v; want %+v", n.RelPath, got, want)
	}
	for _, c := range n.Children {
		assertTotals(t, c)
	}
}

func entries(spec ...string) []diffmodel.ListedEntry {
	out := make([]diffmodel.ListedEntry, 0, len(spec))
	for _, s := range spec {
		e := diffmodel.ListedEntry{Name: s, Type: diffmodel.File}
		switch {
		case len(s) > 1 && s[len(s)-1] == '/':
			e.Name, e.Type = s[:len(s)-1], diffmodel.Dir
		case len(s) > 1 && s[len(s)-1] == '@':
			e.Name, e.Type = s[:len(s)-1], diffmodel.Symlink
		}
		out = append(out, e)
	}
	return out
}

func TestTotalsCountEveryEntryType(t *testing.T) {
	tr := NewTree(diffmodel.Left)
	tr.ApplyListing(tr.Root, entries("sub/", "gone/", "added.txt", "link@"), nil)
	tr.ApplyListing(tr.Index["sub"], entries("deep.txt"), nil)

	got := tr.Root.Totals()
	if got.Dirs != 2 {
		t.Errorf("dirs = %d; want 2", got.Dirs)
	}
	if got.Files != 2 {
		t.Errorf("files = %d; want 2 — the nested file counts too", got.Files)
	}
	if got.Symlinks != 1 {
		t.Errorf("symlinks = %d; want 1 — counted apart from files, since they're never sized", got.Symlinks)
	}
	if got.SizedFiles != 0 || got.Size != 0 {
		t.Errorf("size = %d over %d files; want nothing known before any metadata has been read", got.Size, got.SizedFiles)
	}
	assertTotals(t, tr.Root)
}

func TestStatAddsSizeToEveryAncestor(t *testing.T) {
	tr := NewTree(diffmodel.Left)
	tr.ApplyListing(tr.Root, entries("sub/"), nil)
	sub := tr.Index["sub"]
	tr.ApplyListing(sub, entries("f.txt"), nil)
	f := tr.Index["sub/f.txt"]

	ApplyStat(f, 100, time.Unix(1, 0))
	for _, n := range []*Node{sub, tr.Root} {
		got := n.Totals()
		if got.Size != 100 || got.SizedFiles != 1 {
			t.Fatalf("%q totals = %d bytes over %d files; want 100 over 1 — the count must reach every ancestor", n.RelPath, got.Size, got.SizedFiles)
		}
	}

	// Re-statting the same file replaces its size instead of adding to it.
	ApplyStat(f, 300, time.Unix(2, 0))
	if got := tr.Root.Totals(); got.Size != 300 || got.SizedFiles != 1 {
		t.Errorf("totals after re-statting = %d bytes over %d files; want 300 over 1", got.Size, got.SizedFiles)
	}
	assertTotals(t, tr.Root)
}

func TestSymlinkNeverContributesASize(t *testing.T) {
	tr := NewTree(diffmodel.Left)
	tr.ApplyListing(tr.Root, entries("link@"), nil)

	// A symlink's stat reports the size of the link itself, not of what it
	// names (SPEC.md §7) — counting it would make the details panel's
	// sized/total ratio stop meaning anything.
	ApplyStat(tr.Index["link"], 11, time.Unix(1, 0))

	if got := tr.Root.Totals(); got.Size != 0 || got.SizedFiles != 0 {
		t.Errorf("symlink contributed size %d over %d files; want none", got.Size, got.SizedFiles)
	}
	assertTotals(t, tr.Root)
}

func TestRelistingReplacesTotalsInsteadOfAddingToThem(t *testing.T) {
	tr := NewTree(diffmodel.Left)
	tr.ApplyListing(tr.Root, entries("sub/"), nil)
	sub := tr.Index["sub"]
	tr.ApplyListing(sub, entries("gone.txt", "f.txt"), nil)
	ApplyStat(tr.Index["sub/gone.txt"], 500, time.Unix(1, 0))

	tr.ApplyListing(sub, entries("f.txt"), nil)

	got := tr.Root.Totals()
	if got.Files != 1 {
		t.Errorf("file count = %d; want 1 — the discarded row is no longer in the tree", got.Files)
	}
	if got.Size != 0 || got.SizedFiles != 0 {
		t.Errorf("totals = %d bytes over %d files; want nothing, since the only sized row was discarded", got.Size, got.SizedFiles)
	}
	if _, ok := tr.Index["sub/gone.txt"]; ok {
		t.Error("the discarded row is still in the index")
	}
	assertTotals(t, tr.Root)
}

// TestTotalsSurviveRandomMutationSequence drives listings and stat
// results in a random order — the way they actually arrive from two
// worker pools — checking the incremental bookkeeping against the walk
// after every step, including repeated stats on the same file.
func TestTotalsSurviveRandomMutationSequence(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	tr := NewTree(diffmodel.Left)

	unlisted := []*Node{tr.Root}
	var statable []*Node

	suffixes := []string{"", "", "@", "/"}

	for step := 0; step < 400 && (len(unlisted) > 0 || len(statable) > 0); step++ {
		if len(unlisted) == 0 || (len(statable) > 0 && rng.Intn(2) == 0) {
			n := statable[rng.Intn(len(statable))]
			ApplyStat(n, rng.Int63n(1<<20), time.Unix(int64(step), 0))
			assertTotals(t, tr.Root)
			continue
		}

		i := rng.Intn(len(unlisted))
		d := unlisted[i]
		unlisted = append(unlisted[:i], unlisted[i+1:]...)

		names := make([]string, 1+rng.Intn(4))
		for j := range names {
			names[j] = fmt.Sprintf("e%d-%d%s", step, j, suffixes[rng.Intn(len(suffixes))])
		}
		if d == tr.Root {
			// The root's own listing is the only one guaranteed to happen,
			// so seed the tree with a subdirectory there rather than leaving
			// it to chance — a flat tree would never exercise propagation
			// through more than one ancestor.
			names = append(names, "sub/")
		}
		tr.ApplyListing(d, entries(names...), nil)
		for _, c := range d.Children {
			switch {
			case c.IsDir() && len(unlisted) < 40:
				unlisted = append(unlisted, c)
			case !c.IsDir():
				statable = append(statable, c)
			}
		}
		assertTotals(t, tr.Root)
	}

	got := tr.Root.Totals()
	if got.SizedFiles == 0 || got.Dirs == 0 || got.Symlinks == 0 {
		t.Fatalf("random sequence produced a degenerate tree (%+v); the check proved nothing", got)
	}
}

func TestPendingListingPropagatesUpward(t *testing.T) {
	tr := NewTree(diffmodel.Left)
	tr.ApplyListing(tr.Root, entries("sub/"), nil)
	sub := tr.Index["sub"]

	AdjustPendingListing(sub, 1)
	if sub.PendingListing != 1 || tr.Root.PendingListing != 1 {
		t.Fatalf("PendingListing = sub:%d root:%d; want 1/1", sub.PendingListing, tr.Root.PendingListing)
	}
	AdjustPendingListing(sub, -1)
	if sub.PendingListing != 0 || tr.Root.PendingListing != 0 {
		t.Fatalf("PendingListing = sub:%d root:%d after the job settled; want 0/0", sub.PendingListing, tr.Root.PendingListing)
	}
}

func TestListingSortsDirsFirstThenAlpha(t *testing.T) {
	tr := NewTree(diffmodel.Left)
	// Handed to the tree in the wrong order on purpose: a pane's order
	// must not depend on whoever produced the listing.
	tr.ApplyListing(tr.Root, entries("zzz.txt", "aaa.txt", "zdir/", "adir/"), nil)

	var names []string
	for _, c := range tr.Root.Children {
		names = append(names, c.Name)
	}
	want := []string{"adir", "zdir", "aaa.txt", "zzz.txt"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("children = %v; want %v", names, want)
	}
}
