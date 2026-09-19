package tree

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
)

// totalsByWalk is the definition Node.descLeft/descRight have to agree
// with at all times: visit every descendant and add up what each one
// counts for on each side.
func totalsByWalk(n *Node) (left, right SideTotals) {
	for _, c := range n.Children {
		own := ownContribution(c)
		left.add(own.left)
		right.add(own.right)
		cl, cr := totalsByWalk(c)
		left.add(cl)
		right.add(cr)
	}
	return left, right
}

func assertTotals(t *testing.T, n *Node) {
	t.Helper()
	wantLeft, wantRight := totalsByWalk(n)
	gotLeft, gotRight := n.DescendantTotals()
	if gotLeft != wantLeft {
		t.Fatalf("node %q: left totals = %+v; want %+v", n.RelPath, gotLeft, wantLeft)
	}
	if gotRight != wantRight {
		t.Fatalf("node %q: right totals = %+v; want %+v", n.RelPath, gotRight, wantRight)
	}
	for _, c := range n.Children {
		assertTotals(t, c)
	}
}

func TestTotalsCountEachSideSeparately(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{
		{Name: "sub", Type: diffmodel.Dir, Presence: diffmodel.Both},
		{Name: "gone", Type: diffmodel.Dir, Presence: diffmodel.LeftOnly},
		{Name: "added.txt", Type: diffmodel.File, Presence: diffmodel.RightOnly},
		{Name: "link", Type: diffmodel.Symlink, Presence: diffmodel.Both},
	}, nil, nil)
	ApplyListing(root.Children[0], []diffmodel.ListedChild{
		{Name: "deep.txt", Type: diffmodel.File, Presence: diffmodel.Both},
	}, nil, nil)

	left, right := root.DescendantTotals()
	if left.Dirs != 2 || right.Dirs != 1 {
		t.Errorf("dirs = %d left / %d right; want 2/1 — the left-only directory counts on the left alone", left.Dirs, right.Dirs)
	}
	if left.Files != 1 || right.Files != 2 {
		t.Errorf("files = %d left / %d right; want 1/2 — the nested file counts on both sides, the right-only one on the right", left.Files, right.Files)
	}
	if left.Symlinks != 1 || right.Symlinks != 1 {
		t.Errorf("symlinks = %d left / %d right; want 1/1 — counted apart from files, since they're never sized", left.Symlinks, right.Symlinks)
	}
	if left.SizedFiles != 0 || left.Size != 0 {
		t.Errorf("left size = %d over %d files; want nothing known before any comparison has run", left.Size, left.SizedFiles)
	}
	assertTotals(t, root)
}

func TestCompareResultAddsSizeToEveryAncestor(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{{Name: "sub", Type: diffmodel.Dir, Presence: diffmodel.Both}}, nil, nil)
	sub := root.Children[0]
	ApplyListing(sub, []diffmodel.ListedChild{{Name: "f.txt", Type: diffmodel.File, Presence: diffmodel.Both}}, nil, nil)
	f := sub.Children[0]

	ApplyCompareResult(f, diffmodel.SizeMtime, diffmodel.Differs, nil, &diffmodel.StatInfo{LeftSize: 100, RightSize: 250})
	for _, n := range []*Node{sub, root} {
		left, right := n.DescendantTotals()
		if left.Size != 100 || right.Size != 250 {
			t.Fatalf("%q size = %d left / %d right; want 100/250 — the metadata level already read both", n.RelPath, left.Size, right.Size)
		}
		if left.SizedFiles != 1 || right.SizedFiles != 1 {
			t.Fatalf("%q sized files = %d left / %d right; want 1/1", n.RelPath, left.SizedFiles, right.SizedFiles)
		}
	}

	// A second, deeper comparison re-reads the same file: its size replaces
	// the earlier one instead of being added to it.
	ApplyCompareResult(f, diffmodel.Checksum, diffmodel.Differs, nil, &diffmodel.StatInfo{LeftSize: 300, RightSize: 250})
	left, _ := root.DescendantTotals()
	if left.Size != 300 || left.SizedFiles != 1 {
		t.Errorf("left totals after re-comparing = %d bytes over %d files; want 300 over 1", left.Size, left.SizedFiles)
	}
	assertTotals(t, root)
}

func TestSymlinkComparisonNeverContributesASize(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{{Name: "link", Type: diffmodel.Symlink, Presence: diffmodel.Both}}, nil, nil)

	// scan.DoCompare reports no stat for a symlink (it compares link
	// targets, SPEC.md §7) — but even handed one, a symlink must not be
	// counted as a sized file, or the details panel's sized/total ratio
	// stops meaning anything.
	ApplyCompareResult(root.Children[0], diffmodel.Checksum, diffmodel.Same, nil, &diffmodel.StatInfo{LeftSize: 11, RightSize: 11})

	left, right := root.DescendantTotals()
	if left.Size != 0 || left.SizedFiles != 0 || right.Size != 0 || right.SizedFiles != 0 {
		t.Errorf("symlink contributed size %d/%d over %d/%d files; want none", left.Size, right.Size, left.SizedFiles, right.SizedFiles)
	}
	assertTotals(t, root)
}

func TestRelistingReplacesTotalsInsteadOfAddingToThem(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{{Name: "sub", Type: diffmodel.Dir, Presence: diffmodel.Both}}, nil, nil)
	sub := root.Children[0]
	ApplyListing(sub, []diffmodel.ListedChild{
		{Name: "gone.txt", Type: diffmodel.File, Presence: diffmodel.Both},
		{Name: "f.txt", Type: diffmodel.File, Presence: diffmodel.Both},
	}, nil, nil)
	ApplyCompareResult(sub.Children[0], diffmodel.SizeMtime, diffmodel.Same, nil, &diffmodel.StatInfo{LeftSize: 500, RightSize: 500})

	ApplyListing(sub, []diffmodel.ListedChild{{Name: "f.txt", Type: diffmodel.File, Presence: diffmodel.Both}}, nil, nil)

	left, _ := root.DescendantTotals()
	if left.Files != 1 {
		t.Errorf("file count = %d; want 1 — the discarded row is no longer in the tree", left.Files)
	}
	if left.Size != 0 || left.SizedFiles != 0 {
		t.Errorf("left totals = %d bytes over %d files; want nothing, since the only sized row was discarded", left.Size, left.SizedFiles)
	}
	assertTotals(t, root)
}

// TestTotalsSurviveRandomMutationSequence drives listings and compare
// results in a random order — the way they actually arrive from the two
// worker pools — checking the incremental bookkeeping against the walk
// after every step, including repeated results on the same file.
func TestTotalsSurviveRandomMutationSequence(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	root := NewRoot()

	unlisted := []*Node{root}
	var comparable []*Node

	presences := []diffmodel.Presence{diffmodel.Both, diffmodel.Both, diffmodel.LeftOnly, diffmodel.RightOnly}
	types := []diffmodel.EntryType{diffmodel.File, diffmodel.File, diffmodel.Symlink, diffmodel.Dir}
	levels := []diffmodel.CompareLevel{diffmodel.SizeMtime, diffmodel.Checksum}

	for step := 0; step < 400 && (len(unlisted) > 0 || len(comparable) > 0); step++ {
		if len(unlisted) == 0 || (len(comparable) > 0 && rng.Intn(2) == 0) {
			n := comparable[rng.Intn(len(comparable))]
			var stat *diffmodel.StatInfo
			// Not every result carries stat metadata: a failed lstat
			// reports none, so the totals have to stay correct across a
			// mix of sized and unsized results for the same row.
			if rng.Intn(4) > 0 {
				stat = &diffmodel.StatInfo{
					LeftSize:  rng.Int63n(1 << 20),
					RightSize: rng.Int63n(1 << 20),
					LeftMtime: time.Unix(int64(step), 0), RightMtime: time.Unix(int64(step), 0),
				}
			}
			ApplyCompareResult(n, levels[rng.Intn(len(levels))], diffmodel.Same, nil, stat)
			assertTotals(t, root)
			assertTallies(t, root)
			continue
		}

		i := rng.Intn(len(unlisted))
		d := unlisted[i]
		unlisted = append(unlisted[:i], unlisted[i+1:]...)

		children := make([]diffmodel.ListedChild, 1+rng.Intn(4))
		for j := range children {
			children[j] = diffmodel.ListedChild{
				Name:     fmt.Sprintf("e%d-%d", step, j),
				Type:     types[rng.Intn(len(types))],
				Presence: presences[rng.Intn(len(presences))],
			}
		}
		if d == root {
			// The root's own listing is the only one guaranteed to happen,
			// so seed the tree with a subdirectory there rather than leaving
			// it to chance — a flat tree would never exercise propagation
			// through more than one ancestor.
			children = append(children, diffmodel.ListedChild{Name: "sub", Type: diffmodel.Dir, Presence: diffmodel.Both})
		}
		ApplyListing(d, children, nil, nil)
		for _, c := range d.Children {
			switch {
			case c.IsDir() && len(unlisted) < 40:
				unlisted = append(unlisted, c)
			case !c.IsDir() && c.Presence == diffmodel.Both:
				comparable = append(comparable, c)
			}
		}
		assertTotals(t, root)
		assertTallies(t, root)
	}

	left, right := root.DescendantTotals()
	if left.SizedFiles == 0 || right.SizedFiles == 0 || left.Dirs == 0 || left.Symlinks == 0 {
		t.Fatalf("random sequence produced a degenerate tree (left %+v, right %+v); the check proved nothing", left, right)
	}
}
