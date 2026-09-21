package pairtree

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/m42cel/dirdiff/internal/diffmodel"
)

// countByWalk is the definition Node.descMatches has to agree with at
// all times: classify every descendant and tally the results. The UI
// used to do exactly this on every render — it lives on here as the
// oracle the incremental bookkeeping is checked against.
func countByWalk(n *Node) statusDelta {
	var out statusDelta
	for _, c := range n.Children {
		if s := c.RowStatus(); s != diffmodel.RowNone {
			out[s]++
		}
		out.add(countByWalk(c))
	}
	return out
}

func assertTallies(t *testing.T, n *Node) {
	t.Helper()
	if got, want := statusDelta(n.descMatches), countByWalk(n); got != want {
		t.Fatalf("node %q: descMatches = %v; want %v", n.PairRel, got, want)
	}
	for _, c := range n.Children {
		assertTallies(t, c)
	}
}

func TestMergeCountsOneSidedEntriesImmediately(t *testing.T) {
	f := newFixture()
	f.list(f.root, leftD("gone"), rightF("new.txt"), bothF("both.txt"))

	if got := f.root.DescendantsWithStatus(diffmodel.RowLeftOnly); got != 1 {
		t.Errorf("left-only tally = %d; want 1 — presence is known as soon as both sides have listed", got)
	}
	if got := f.root.DescendantsWithStatus(diffmodel.RowRightOnly); got != 1 {
		t.Errorf("right-only tally = %d; want 1", got)
	}
	// A matched entry has no status until it's actually been compared.
	if got := f.root.DescendantsWithStatus(diffmodel.RowEqual); got != 0 {
		t.Errorf("equal tally = %d; want 0 before any comparison has run", got)
	}
	if got := f.root.DescendantsWithStatus(diffmodel.RowDifferent); got != 0 {
		t.Errorf("different tally = %d; want 0 before any comparison has run", got)
	}
	assertTallies(t, f.root)
}

func TestCompareResultMovesTallyBetweenStatuses(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothD("sub"))
	sub := f.find("sub")
	f.list(sub, bothF("f.txt"))
	file := f.find("sub/f.txt")

	ApplyCompareResult(file, diffmodel.SizeMtime, diffmodel.Same, nil)
	for _, n := range []*Node{sub, f.root} {
		if got := n.DescendantsWithStatus(diffmodel.RowEqual); got != 1 {
			t.Fatalf("%q equal tally = %d; want 1 — the count must reach every ancestor", n.PairRel, got)
		}
	}

	// A deeper level overriding a shallower verdict moves the row from
	// one status to the other, not into both.
	ApplyCompareResult(file, diffmodel.Checksum, diffmodel.Differs, nil)
	for _, n := range []*Node{sub, f.root} {
		if got := n.DescendantsWithStatus(diffmodel.RowEqual); got != 0 {
			t.Errorf("%q equal tally = %d; want 0 once the row differs", n.PairRel, got)
		}
		if got := n.DescendantsWithStatus(diffmodel.RowDifferent); got != 1 {
			t.Errorf("%q different tally = %d; want 1", n.PairRel, got)
		}
	}
	assertTallies(t, f.root)
}

func TestStaleCompareResultLeavesTalliesAlone(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothF("f.txt"))
	file := f.find("f.txt")

	ApplyCompareResult(file, diffmodel.Checksum, diffmodel.Differs, nil)
	// Shallower, stale result: a no-op on Result (SPEC.md §5.3), so it
	// must be a no-op on the tallies too.
	ApplyCompareResult(file, diffmodel.SizeMtime, diffmodel.Same, nil)

	if got := f.root.DescendantsWithStatus(diffmodel.RowDifferent); got != 1 {
		t.Errorf("different tally = %d; want 1", got)
	}
	if got := f.root.DescendantsWithStatus(diffmodel.RowEqual); got != 0 {
		t.Errorf("equal tally = %d; want 0", got)
	}
	assertTallies(t, f.root)
}

func TestMergeFoldsInAnAlreadyListedSubtree(t *testing.T) {
	// The rows under "sub" are created in the same merge as "sub" itself
	// (its two sides were listed before the row existed), so their whole
	// tally has to land on the new ancestors, not just sub's own status.
	f := newFixture()
	f.listSide(f.root, diffmodel.Left, bothD("sub"))
	leftTree := f.trees[diffmodel.Left]
	leftTree.ApplyListing(leftTree.Index["sub"], []diffmodel.ListedEntry{
		{Name: "a.txt", Type: diffmodel.File},
		{Name: "b.txt", Type: diffmodel.File},
	}, nil)

	rightTree := f.trees[diffmodel.Right]
	rightTree.ApplyListing(rightTree.Root, []diffmodel.ListedEntry{{Name: "sub", Type: diffmodel.Dir}}, nil)
	rightTree.ApplyListing(rightTree.Index["sub"], []diffmodel.ListedEntry{{Name: "a.txt", Type: diffmodel.File}}, nil)
	f.merge(f.root)

	ApplyCompareResult(f.find("sub/a.txt"), diffmodel.SizeMtime, diffmodel.Same, nil)

	if got := f.root.DescendantsWithStatus(diffmodel.RowEqual); got != 1 {
		t.Errorf("equal tally = %d; want 1", got)
	}
	if got := f.root.DescendantsWithStatus(diffmodel.RowLeftOnly); got != 1 {
		t.Errorf("left-only tally = %d; want 1", got)
	}
	assertTallies(t, f.root)
}

func TestDirectoryRollupNeverCountsAsEqualOrDifferent(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothD("sub"))
	sub := f.find("sub")
	f.list(sub, bothF("f.txt"))
	ApplyCompareResult(f.find("sub/f.txt"), diffmodel.SizeMtime, diffmodel.Differs, nil)

	if sub.Result != diffmodel.Differs {
		t.Fatalf("sub.Result = %v; want the rollup to reach Differs", sub.Result)
	}
	// One differing row exists in the tree, not two: sub's own rollup is
	// not itself a "different" row (SPEC.md §4.7).
	if got := f.root.DescendantsWithStatus(diffmodel.RowDifferent); got != 1 {
		t.Errorf("different tally = %d; want 1 — a directory's rollup must not add a row of its own", got)
	}
	assertTallies(t, f.root)
}

// TestTalliesSurviveRandomMutationSequence drives listings and compare
// results in a random order — the way they actually arrive from two
// worker pools — and checks the incremental bookkeeping against the
// walk after every single step.
func TestTalliesSurviveRandomMutationSequence(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	f := newFixture()

	unlisted := []*Node{f.root} // directories still waiting for a listing
	var files []*Node           // matched rows a compare job can land on

	presences := []diffmodel.Presence{diffmodel.Both, diffmodel.Both, diffmodel.LeftOnly, diffmodel.RightOnly}
	results := []diffmodel.CompareResult{diffmodel.Same, diffmodel.Differs, diffmodel.CompareError}
	levels := []diffmodel.CompareLevel{diffmodel.SizeMtime, diffmodel.Checksum}

	for step := 0; step < 400 && (len(unlisted) > 0 || len(files) > 0); step++ {
		// Interleave the two kinds of result the way the two worker
		// pools do, rather than listing the whole tree up front.
		if len(unlisted) == 0 || (len(files) > 0 && rng.Intn(2) == 0) {
			n := files[rng.Intn(len(files))]
			ApplyCompareResult(n, levels[rng.Intn(len(levels))], results[rng.Intn(len(results))], nil)
			assertTallies(t, f.root)
			continue
		}

		i := rng.Intn(len(unlisted))
		d := unlisted[i]
		unlisted = append(unlisted[:i], unlisted[i+1:]...)

		children := make([]entry, 1+rng.Intn(4))
		for j := range children {
			typ := diffmodel.File
			// Keep directories rare enough that the tree stays shallow
			// but deep enough to exercise multi-level propagation.
			if rng.Intn(4) == 0 {
				typ = diffmodel.Dir
			}
			children[j] = entry{
				name:     fmt.Sprintf("e%d-%d", step, j),
				typ:      typ,
				presence: presences[rng.Intn(len(presences))],
			}
		}
		f.list(d, children...)
		for _, c := range d.Children {
			switch {
			case c.IsDir() && len(unlisted) < 40:
				unlisted = append(unlisted, c)
			case !c.IsDir() && c.Presence() == diffmodel.Both:
				files = append(files, c)
			}
		}
		assertTallies(t, f.root)
	}

	if len(files) == 0 || f.root.DescendantsWithStatus(diffmodel.RowLeftOnly) == 0 {
		t.Fatalf("random sequence produced a degenerate tree (%d comparable files, %d left-only); the check proved nothing",
			len(files), f.root.DescendantsWithStatus(diffmodel.RowLeftOnly))
	}
}
