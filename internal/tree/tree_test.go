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
	ApplyCompareResult(n, diffmodel.Size, diffmodel.Same, nil, nil)

	if n.Level != diffmodel.Checksum || n.Result != diffmodel.Differs {
		t.Fatalf("Level=%v Result=%v; want Checksum/Differs to survive the shallower re-trigger", n.Level, n.Result)
	}
}

func TestApplyCompareResultAlwaysRecordsStat(t *testing.T) {
	n := &Node{Type: diffmodel.File}
	ApplyCompareResult(n, diffmodel.Checksum, diffmodel.Same, nil, &diffmodel.StatInfo{LeftSize: 1, RightSize: 1})
	// A shallower re-trigger shouldn't touch Level/Result but should still refresh stat info.
	ApplyCompareResult(n, diffmodel.Size, diffmodel.Same, nil, &diffmodel.StatInfo{LeftSize: 2, RightSize: 2})

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

	if root.Rollup != diffmodel.Unknown {
		t.Fatalf("root.Rollup = %v before any compare; want Unknown", root.Rollup)
	}

	ApplyCompareResult(f, diffmodel.Checksum, diffmodel.Differs, nil, nil)

	if f.Result != diffmodel.Differs {
		t.Fatalf("f.Result = %v; want Differs", f.Result)
	}
	if b.Rollup != diffmodel.Differs {
		t.Fatalf("b.Rollup = %v; want Differs", b.Rollup)
	}
	if a.Rollup != diffmodel.Differs {
		t.Fatalf("a.Rollup (grandparent) = %v; want Differs to propagate all the way up", a.Rollup)
	}
	if root.Rollup != diffmodel.Differs {
		t.Fatalf("root.Rollup = %v; want Differs", root.Rollup)
	}
}

func TestRollupTreatsOneSidedChildAsDiffers(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{
		{Name: "onlyleft.txt", Type: diffmodel.File, Presence: diffmodel.LeftOnly},
	}, nil, nil)

	if root.Rollup != diffmodel.Differs {
		t.Fatalf("root.Rollup = %v; want Differs for a one-sided child, even with no compare run", root.Rollup)
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
	if root.Rollup != diffmodel.Same {
		t.Fatalf("root.Rollup = %v; want Same when every compared child is Same", root.Rollup)
	}
}

func TestRollupSameWhenEmptyOnBothSides(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{
		{Name: "empty", Type: diffmodel.Dir, Presence: diffmodel.Both},
	}, nil, nil)
	empty := root.Children[0]
	ApplyListing(empty, nil, nil, nil)

	if empty.Rollup != diffmodel.Same {
		t.Fatalf("empty.Rollup = %v; want Same for a directory listed on both sides with no children", empty.Rollup)
	}
	if root.Rollup != diffmodel.Same {
		t.Fatalf("root.Rollup = %v; want Same to propagate up through an empty-both-sides child", root.Rollup)
	}
}

func TestRollupListErrIsError(t *testing.T) {
	root := NewRoot()
	ApplyListing(root, []diffmodel.ListedChild{{Name: "a", Type: diffmodel.Dir, Presence: diffmodel.Both}}, nil, nil)
	a := root.Children[0]
	ApplyListing(a, nil, errPermission, nil)

	if a.Rollup != diffmodel.CompareError {
		t.Fatalf("a.Rollup = %v; want CompareError when the directory itself failed to list", a.Rollup)
	}
}

var errPermission = &permErr{}

type permErr struct{}

func (*permErr) Error() string { return "permission denied" }
