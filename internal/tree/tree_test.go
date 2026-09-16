package tree

import (
	"testing"

	"github.com/m42cel/dirdiff/internal/diffmodel"
)

func TestApplyListingBuildsChildrenWithRelPath(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{
		{Name: "sub", Type: diffmodel.Dir, Presence: diffmodel.Both},
		{Name: "f.txt", Type: diffmodel.File, Presence: diffmodel.Both},
	}, nil, nil)

	if len(root.Children) != 2 {
		t.Fatalf("got %d children; want 2", len(root.Children))
	}
	// dirs sort first
	if root.Children[0].Name != "sub" || root.Children[0].RelPath != "sub" {
		t.Errorf("child[0] = %+v; want sub", root.Children[0])
	}
	if root.Children[0].Parent != root {
		t.Error("child.Parent not set to root")
	}

	sub := root.Children[0]
	ApplyListing(sub, []diffmodel.ListedChild{
		{Name: "nested.txt", Type: diffmodel.File, Presence: diffmodel.Both},
	}, nil, nil)
	if sub.Children[0].RelPath != "sub/nested.txt" {
		t.Errorf("RelPath = %q; want %q", sub.Children[0].RelPath, "sub/nested.txt")
	}
}

func TestApplyCompareResultMonotonic(t *testing.T) {
	n := &Node{Type: diffmodel.File}
	ApplyCompareResult(n, diffmodel.Checksum, diffmodel.Differs, nil, nil)
	// A shallower, stale result arriving afterward must not downgrade the display.
	ApplyCompareResult(n, diffmodel.SizeMtime, diffmodel.Same, nil, nil)

	if n.Level != diffmodel.Checksum || n.Result != diffmodel.Differs {
		t.Fatalf("Level=%v Result=%v; want Checksum/Differs to survive the shallower re-trigger", n.Level, n.Result)
	}
}

func TestApplyCompareResultAlwaysRecordsStat(t *testing.T) {
	n := &Node{Type: diffmodel.File}
	ApplyCompareResult(n, diffmodel.Checksum, diffmodel.Same, nil, &diffmodel.StatInfo{LeftSize: 1, RightSize: 1})
	// A shallower re-trigger shouldn't touch Level/Result but should still refresh stat info.
	ApplyCompareResult(n, diffmodel.SizeMtime, diffmodel.Same, nil, &diffmodel.StatInfo{LeftSize: 2, RightSize: 2})

	if !n.HaveStat || n.LeftSize != 2 {
		t.Fatalf("stat not updated: HaveStat=%v LeftSize=%d", n.HaveStat, n.LeftSize)
	}
	if n.Level != diffmodel.Checksum {
		t.Fatalf("Level = %v; want Checksum to remain the deepest known level", n.Level)
	}
}

func TestRollupPropagatesUpwardOnDifference(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{{Name: "a", Type: diffmodel.Dir, Presence: diffmodel.Both}}, nil, nil)
	a := root.Children[0]
	ApplyListing(a, []diffmodel.ListedChild{{Name: "b", Type: diffmodel.Dir, Presence: diffmodel.Both}}, nil, nil)
	b := a.Children[0]
	ApplyListing(b, []diffmodel.ListedChild{{Name: "f.txt", Type: diffmodel.File, Presence: diffmodel.Both}}, nil, nil)
	f := b.Children[0]

	if root.Result != diffmodel.Unknown {
		t.Fatalf("root.Result = %v before any compare; want Unknown", root.Result)
	}

	ApplyCompareResult(f, diffmodel.Checksum, diffmodel.Differs, nil, nil)

	if f.Result != diffmodel.Differs {
		t.Fatalf("f.Result = %v; want Differs", f.Result)
	}
	if b.Result != diffmodel.Differs {
		t.Fatalf("b.Result = %v; want Differs", b.Result)
	}
	if a.Result != diffmodel.Differs {
		t.Fatalf("a.Result (grandparent) = %v; want Differs to propagate all the way up", a.Result)
	}
	if root.Result != diffmodel.Differs {
		t.Fatalf("root.Result = %v; want Differs", root.Result)
	}
}

func TestRollupTreatsOneSidedChildAsDiffers(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{
		{Name: "onlyleft.txt", Type: diffmodel.File, Presence: diffmodel.LeftOnly},
	}, nil, nil)

	if root.Result != diffmodel.Differs {
		t.Fatalf("root.Result = %v; want Differs for a one-sided child, even with no compare run", root.Result)
	}
}

func TestRollupCleanWhenAllSame(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{
		{Name: "a.txt", Type: diffmodel.File, Presence: diffmodel.Both},
		{Name: "b.txt", Type: diffmodel.File, Presence: diffmodel.Both},
	}, nil, nil)
	for _, c := range root.Children {
		ApplyCompareResult(c, diffmodel.Checksum, diffmodel.Same, nil, nil)
	}
	if root.Result != diffmodel.Same {
		t.Fatalf("root.Result = %v; want Same when every compared child is Same", root.Result)
	}
}

func TestRollupSameWhenEmptyOnBothSides(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{
		{Name: "empty", Type: diffmodel.Dir, Presence: diffmodel.Both},
	}, nil, nil)
	empty := root.Children[0]
	ApplyListing(empty, nil, nil, nil)

	if empty.Result != diffmodel.Same {
		t.Fatalf("empty.Result = %v; want Same for a directory listed on both sides with no children", empty.Result)
	}
	if root.Result != diffmodel.Same {
		t.Fatalf("root.Result = %v; want Same to propagate up through an empty-both-sides child", root.Result)
	}
}

// TestRollupUncomparedOutranksSame guards against a vacuously-Same empty
// subdirectory dragging an otherwise-uncompared parent down to Same: an
// empty dir sibling contributes nothing to say about the uncompared
// sibling, so the parent must stay Unknown ("not compared") rather than
// reporting Same before the other child has been checked at all.
func TestRollupUncomparedOutranksSame(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{
		{Name: "empty", Type: diffmodel.Dir, Presence: diffmodel.Both},
		{Name: "f.txt", Type: diffmodel.File, Presence: diffmodel.Both},
	}, nil, nil)
	empty := root.Children[0]
	ApplyListing(empty, nil, nil, nil)

	if empty.Result != diffmodel.Same {
		t.Fatalf("empty.Result = %v; want Same for a directory listed on both sides with no children", empty.Result)
	}
	if root.Result != diffmodel.Unknown {
		t.Fatalf("root.Result = %v; want Unknown while f.txt hasn't been compared yet, even though the empty sibling is Same", root.Result)
	}
}

func TestRollupListErrIsError(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{{Name: "a", Type: diffmodel.Dir, Presence: diffmodel.Both}}, nil, nil)
	a := root.Children[0]
	ApplyListing(a, nil, errPermission, nil)

	if a.Result != diffmodel.CompareError {
		t.Fatalf("a.Result = %v; want CompareError when the directory itself failed to list", a.Result)
	}
}

func TestRollupLevelUniformAcrossChildren(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{
		{Name: "a.txt", Type: diffmodel.File, Presence: diffmodel.Both},
		{Name: "b.txt", Type: diffmodel.File, Presence: diffmodel.Both},
	}, nil, nil)
	for _, c := range root.Children {
		ApplyCompareResult(c, diffmodel.Checksum, diffmodel.Same, nil, nil)
	}
	if root.Level != diffmodel.Checksum || root.LevelMixed {
		t.Fatalf("root.Level=%v LevelMixed=%v; want Checksum/false when every child was compared at the same level", root.Level, root.LevelMixed)
	}
}

func TestRollupLevelMixedAcrossChildren(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{
		{Name: "a.txt", Type: diffmodel.File, Presence: diffmodel.Both},
		{Name: "b.txt", Type: diffmodel.File, Presence: diffmodel.Both},
	}, nil, nil)
	ApplyCompareResult(root.Children[0], diffmodel.SizeMtime, diffmodel.Same, nil, nil)
	ApplyCompareResult(root.Children[1], diffmodel.Checksum, diffmodel.Same, nil, nil)

	if !root.LevelMixed {
		t.Fatalf("root.LevelMixed = false; want true when children were compared at different levels")
	}
	if root.Level != diffmodel.Checksum {
		t.Fatalf("root.Level = %v; want Checksum, the deepest level seen", root.Level)
	}
}

func TestRollupLevelIgnoresUncomparedAndOneSidedChildren(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{
		{Name: "a.txt", Type: diffmodel.File, Presence: diffmodel.Both},
		{Name: "b.txt", Type: diffmodel.File, Presence: diffmodel.Both},
		{Name: "onlyleft.txt", Type: diffmodel.File, Presence: diffmodel.LeftOnly},
	}, nil, nil)
	ApplyCompareResult(root.Children[0], diffmodel.SizeMtime, diffmodel.Same, nil, nil)
	// b.txt is left NotCompared, and onlyleft.txt can never be compared.

	if root.LevelMixed {
		t.Fatalf("root.LevelMixed = true; want false — an uncompared or one-sided child shouldn't count as a disagreement")
	}
	if root.Level != diffmodel.SizeMtime {
		t.Fatalf("root.Level = %v; want SizeMtime, the only level actually observed", root.Level)
	}
}

func TestRollupLevelPropagatesFromNestedDirectory(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{{Name: "a", Type: diffmodel.Dir, Presence: diffmodel.Both}}, nil, nil)
	a := root.Children[0]
	ApplyListing(a, []diffmodel.ListedChild{
		{Name: "x.txt", Type: diffmodel.File, Presence: diffmodel.Both},
		{Name: "y.txt", Type: diffmodel.File, Presence: diffmodel.Both},
	}, nil, nil)
	ApplyCompareResult(a.Children[0], diffmodel.SizeMtime, diffmodel.Same, nil, nil)
	ApplyCompareResult(a.Children[1], diffmodel.Checksum, diffmodel.Same, nil, nil)

	if !a.LevelMixed {
		t.Fatalf("a.LevelMixed = false; want true")
	}
	if !root.LevelMixed {
		t.Fatalf("root.LevelMixed = false; want the mixed flag to propagate up through a directory child")
	}
}

var errPermission = &permErr{}

type permErr struct{}

func (*permErr) Error() string { return "permission denied" }
