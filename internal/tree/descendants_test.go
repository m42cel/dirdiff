package tree

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
		t.Fatalf("node %q: descMatches = %v; want %v", n.RelPath, got, want)
	}
	for _, c := range n.Children {
		assertTallies(t, c)
	}
}

func TestListingCountsOneSidedEntriesImmediately(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{
		{Name: "gone", Type: diffmodel.Dir, Presence: diffmodel.LeftOnly},
		{Name: "new.txt", Type: diffmodel.File, Presence: diffmodel.RightOnly},
		{Name: "both.txt", Type: diffmodel.File, Presence: diffmodel.Both},
	}, nil, nil)

	if got := root.DescendantsWithStatus(diffmodel.RowLeftOnly); got != 1 {
		t.Errorf("left-only tally = %d; want 1 — presence is known as soon as the entry is listed", got)
	}
	if got := root.DescendantsWithStatus(diffmodel.RowRightOnly); got != 1 {
		t.Errorf("right-only tally = %d; want 1", got)
	}
	// A matched entry has no status until it's actually been compared.
	if got := root.DescendantsWithStatus(diffmodel.RowEqual); got != 0 {
		t.Errorf("equal tally = %d; want 0 before any comparison has run", got)
	}
	if got := root.DescendantsWithStatus(diffmodel.RowDifferent); got != 0 {
		t.Errorf("different tally = %d; want 0 before any comparison has run", got)
	}
	assertTallies(t, root)
}

func TestCompareResultMovesTallyBetweenStatuses(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{{Name: "sub", Type: diffmodel.Dir, Presence: diffmodel.Both}}, nil, nil)
	sub := root.Children[0]
	ApplyListing(sub, []diffmodel.ListedChild{{Name: "f.txt", Type: diffmodel.File, Presence: diffmodel.Both}}, nil, nil)
	f := sub.Children[0]

	ApplyCompareResult(f, diffmodel.SizeMtime, diffmodel.Same, nil, nil)
	for _, n := range []*Node{sub, root} {
		if got := n.DescendantsWithStatus(diffmodel.RowEqual); got != 1 {
			t.Fatalf("%q equal tally = %d; want 1 — the count must reach every ancestor", n.RelPath, got)
		}
	}

	// A deeper level overriding a shallower verdict moves the row from
	// one status to the other, not into both.
	ApplyCompareResult(f, diffmodel.Checksum, diffmodel.Differs, nil, nil)
	for _, n := range []*Node{sub, root} {
		if got := n.DescendantsWithStatus(diffmodel.RowEqual); got != 0 {
			t.Errorf("%q equal tally = %d; want 0 once the row differs", n.RelPath, got)
		}
		if got := n.DescendantsWithStatus(diffmodel.RowDifferent); got != 1 {
			t.Errorf("%q different tally = %d; want 1", n.RelPath, got)
		}
	}
	assertTallies(t, root)
}

func TestStaleCompareResultLeavesTalliesAlone(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{{Name: "f.txt", Type: diffmodel.File, Presence: diffmodel.Both}}, nil, nil)
	f := root.Children[0]

	ApplyCompareResult(f, diffmodel.Checksum, diffmodel.Differs, nil, nil)
	// Shallower, stale result: a no-op on Result (SPEC.md §5.3), so it
	// must be a no-op on the tallies too.
	ApplyCompareResult(f, diffmodel.SizeMtime, diffmodel.Same, nil, nil)

	if got := root.DescendantsWithStatus(diffmodel.RowDifferent); got != 1 {
		t.Errorf("different tally = %d; want 1", got)
	}
	if got := root.DescendantsWithStatus(diffmodel.RowEqual); got != 0 {
		t.Errorf("equal tally = %d; want 0", got)
	}
	assertTallies(t, root)
}

func TestAddChildFoldsInAnExistingSubtree(t *testing.T) {
	// A subtree assembled on its own, then attached: its whole tally has
	// to land on the new ancestors, not just the child's own status.
	sub := &Node{Name: "sub", Type: diffmodel.Dir, Presence: diffmodel.Both}
	AddChild(sub, &Node{Name: "a.txt", Type: diffmodel.File, Presence: diffmodel.Both, Result: diffmodel.Same})
	AddChild(sub, &Node{Name: "b.txt", Type: diffmodel.File, Presence: diffmodel.LeftOnly})

	root := NewRoot()
	AddChild(root, sub)

	if got := root.DescendantsWithStatus(diffmodel.RowEqual); got != 1 {
		t.Errorf("equal tally = %d; want 1", got)
	}
	if got := root.DescendantsWithStatus(diffmodel.RowLeftOnly); got != 1 {
		t.Errorf("left-only tally = %d; want 1", got)
	}
	assertTallies(t, root)
}

func TestDirectoryRollupNeverCountsAsEqualOrDifferent(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{{Name: "sub", Type: diffmodel.Dir, Presence: diffmodel.Both}}, nil, nil)
	sub := root.Children[0]
	ApplyListing(sub, []diffmodel.ListedChild{{Name: "f.txt", Type: diffmodel.File, Presence: diffmodel.Both}}, nil, nil)
	ApplyCompareResult(sub.Children[0], diffmodel.SizeMtime, diffmodel.Differs, nil, nil)

	if sub.Result != diffmodel.Differs {
		t.Fatalf("sub.Result = %v; want the rollup to reach Differs", sub.Result)
	}
	// One differing row exists in the tree, not two: sub's own rollup is
	// not itself a "different" row (SPEC.md §4.7).
	if got := root.DescendantsWithStatus(diffmodel.RowDifferent); got != 1 {
		t.Errorf("different tally = %d; want 1 — a directory's rollup must not add a row of its own", got)
	}
	assertTallies(t, root)
}

// TestTalliesSurviveRandomMutationSequence drives listings and compare
// results in a random order — the way they actually arrive from two
// worker pools — and checks the incremental bookkeeping against the
// walk after every single step.
func TestTalliesSurviveRandomMutationSequence(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	root := NewRoot()

	unlisted := []*Node{root} // directories still waiting for a listing
	var files []*Node         // matched rows a compare job can land on

	presences := []diffmodel.Presence{diffmodel.Both, diffmodel.Both, diffmodel.LeftOnly, diffmodel.RightOnly}
	results := []diffmodel.CompareResult{diffmodel.Same, diffmodel.Differs, diffmodel.CompareError}
	levels := []diffmodel.CompareLevel{diffmodel.SizeMtime, diffmodel.Checksum}

	for step := 0; step < 400 && (len(unlisted) > 0 || len(files) > 0); step++ {
		// Interleave the two kinds of result the way the two worker
		// pools do, rather than listing the whole tree up front.
		if len(unlisted) == 0 || (len(files) > 0 && rng.Intn(2) == 0) {
			f := files[rng.Intn(len(files))]
			ApplyCompareResult(f, levels[rng.Intn(len(levels))], results[rng.Intn(len(results))], nil, nil)
			assertTallies(t, root)
			continue
		}

		i := rng.Intn(len(unlisted))
		d := unlisted[i]
		unlisted = append(unlisted[:i], unlisted[i+1:]...)

		children := make([]diffmodel.ListedChild, 1+rng.Intn(4))
		for j := range children {
			typ := diffmodel.File
			// Keep directories rare enough that the tree stays shallow
			// but deep enough to exercise multi-level propagation.
			if rng.Intn(4) == 0 {
				typ = diffmodel.Dir
			}
			children[j] = diffmodel.ListedChild{
				Name:     fmt.Sprintf("e%d-%d", step, j),
				Type:     typ,
				Presence: presences[rng.Intn(len(presences))],
			}
		}
		ApplyListing(d, children, nil, nil)
		for _, c := range d.Children {
			switch {
			case c.IsDir() && len(unlisted) < 40:
				unlisted = append(unlisted, c)
			case !c.IsDir() && c.Presence == diffmodel.Both:
				files = append(files, c)
			}
		}
		assertTallies(t, root)
	}

	if len(files) == 0 || root.DescendantsWithStatus(diffmodel.RowLeftOnly) == 0 {
		t.Fatalf("random sequence produced a degenerate tree (%d comparable files, %d left-only); the check proved nothing",
			len(files), root.DescendantsWithStatus(diffmodel.RowLeftOnly))
	}
}

// A directory is only listed once today, but ApplyListing discards
// whatever children it had, so it must not leave their tallies behind
// on the ancestors if that ever changes.
func TestRelistingReplacesTalliesInsteadOfAddingToThem(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{{Name: "sub", Type: diffmodel.Dir, Presence: diffmodel.Both}}, nil, nil)
	sub := root.Children[0]
	ApplyListing(sub, []diffmodel.ListedChild{
		{Name: "gone.txt", Type: diffmodel.File, Presence: diffmodel.LeftOnly},
		{Name: "f.txt", Type: diffmodel.File, Presence: diffmodel.Both},
	}, nil, nil)
	ApplyCompareResult(sub.Children[1], diffmodel.SizeMtime, diffmodel.Differs, nil, nil)

	ApplyListing(sub, []diffmodel.ListedChild{
		{Name: "f.txt", Type: diffmodel.File, Presence: diffmodel.Both},
	}, nil, nil)

	if got := root.DescendantsWithStatus(diffmodel.RowLeftOnly); got != 0 {
		t.Errorf("left-only tally = %d; want 0 — the row it counted is no longer in the tree", got)
	}
	if got := root.DescendantsWithStatus(diffmodel.RowDifferent); got != 0 {
		t.Errorf("different tally = %d; want 0 — the re-listed row hasn't been compared yet", got)
	}
	assertTallies(t, root)
}
