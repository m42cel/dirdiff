package pairtree

import (
	"testing"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/sidetree"
)

func TestMergeBuildsRowsWithPairRelativePaths(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothD("sub"), bothF("f.txt"))

	if len(f.root.Children) != 2 {
		t.Fatalf("got %d children; want 2", len(f.root.Children))
	}
	// dirs sort first
	sub := f.root.Children[0]
	if sub.Name() != "sub" || sub.PairRel != "sub" {
		t.Errorf("child[0] = %q at %q; want sub", sub.Name(), sub.PairRel)
	}
	if sub.Parent != f.root {
		t.Error("child.Parent not set to the root")
	}

	f.list(sub, bothF("nested.txt"))
	if got := sub.Children[0].PairRel; got != "sub/nested.txt" {
		t.Errorf("PairRel = %q; want %q", got, "sub/nested.txt")
	}
}

func TestMergeMatchesByNameAndType(t *testing.T) {
	f := newFixture()
	// "clash" is a directory on the left and a file on the right: matched
	// independently (SPEC.md §3.1), never merged into one row.
	f.list(f.root, bothF("both.txt"), leftD("clash"), rightF("clash"), leftF("gone.txt"), rightF("new.txt"))

	want := map[string]diffmodel.Presence{
		"both.txt": diffmodel.Both,
		"gone.txt": diffmodel.LeftOnly,
		"new.txt":  diffmodel.RightOnly,
	}
	got := map[string]diffmodel.Presence{}
	clash := 0
	for _, c := range f.root.Children {
		if c.Name() == "clash" {
			clash++
			switch c.Type {
			case diffmodel.Dir:
				if c.Presence() != diffmodel.LeftOnly {
					t.Errorf("clash dir: Presence = %v; want LeftOnly", c.Presence())
				}
			case diffmodel.File:
				if c.Presence() != diffmodel.RightOnly {
					t.Errorf("clash file: Presence = %v; want RightOnly", c.Presence())
				}
			}
			continue
		}
		got[c.Name()] = c.Presence()
	}
	if clash != 2 {
		t.Fatalf("expected 2 'clash' rows (dir + file), got %d", clash)
	}
	for name, p := range want {
		if got[name] != p {
			t.Errorf("%s: Presence = %v; want %v", name, got[name], p)
		}
	}
}

func TestMergeWaitsForBothSidesToList(t *testing.T) {
	f := newFixture()
	f.listSide(f.root, diffmodel.Left, bothF("a.txt"), leftF("gone.txt"))

	// Showing the left side's entries now would flash them as left-only
	// and then upgrade them to both-sided once the right side lands, which
	// is exactly what the gating exists to avoid.
	if len(f.root.Children) != 0 {
		t.Fatalf("got %d rows with only one side listed; want none until both have", len(f.root.Children))
	}
	if f.root.Listed() {
		t.Error("Listed() = true with the right side's listing still outstanding")
	}
	if f.root.Result != diffmodel.Unknown {
		t.Errorf("Result = %v while half-listed; want Unknown — nothing is known about the subtree yet", f.root.Result)
	}

	f.listSide(f.root, diffmodel.Right, bothF("a.txt"), leftF("gone.txt"))
	if len(f.root.Children) != 2 {
		t.Fatalf("got %d rows once both sides listed; want 2", len(f.root.Children))
	}
	if got := f.find("a.txt").Presence(); got != diffmodel.Both {
		t.Errorf("a.txt Presence = %v; want Both", got)
	}
	if got := f.find("gone.txt").Presence(); got != diffmodel.LeftOnly {
		t.Errorf("gone.txt Presence = %v; want LeftOnly", got)
	}
}

func TestMergeOfOneSidedDirectoryNeedsOnlyThatSide(t *testing.T) {
	f := newFixture()
	f.list(f.root, leftD("onlyleft"))
	onlyleft := f.find("onlyleft")

	// There is no right side to wait for, so one listing is the whole
	// story — anything else would leave a one-sided directory unopenable.
	f.list(onlyleft, leftF("inside.txt"))
	if !onlyleft.Listed() {
		t.Fatal("a left-only directory listed on the left must count as listed")
	}
	if len(onlyleft.Children) != 1 || onlyleft.Children[0].Presence() != diffmodel.LeftOnly {
		t.Fatalf("children = %v; want one left-only row", onlyleft.Children)
	}
}

func TestMergeCatchesUpWithAnAlreadyListedSubtree(t *testing.T) {
	// The left side races ahead and lists a whole subtree before the right
	// side's parent listing lands, so the rows for it are created after
	// their listing results have already come and gone. A pairing opened
	// over an already-scanned subtree is the same situation.
	f := newFixture()
	f.listSide(f.root, diffmodel.Left, bothD("sub"))
	leftTree := f.trees[diffmodel.Left]
	leftTree.ApplyListing(leftTree.Index["sub"], []diffmodel.ListedEntry{{Name: "deep.txt", Type: diffmodel.File}}, nil)

	rightTree := f.trees[diffmodel.Right]
	rightTree.ApplyListing(rightTree.Root, []diffmodel.ListedEntry{{Name: "sub", Type: diffmodel.Dir}}, nil)
	rightTree.ApplyListing(rightTree.Index["sub"], []diffmodel.ListedEntry{{Name: "deep.txt", Type: diffmodel.File}}, nil)
	f.merge(f.root)

	deep := f.find("sub/deep.txt")
	if deep == nil {
		t.Fatal("sub/deep.txt has no row; the merge stopped at the directory it was called on")
	}
	if deep.Presence() != diffmodel.Both {
		t.Errorf("sub/deep.txt Presence = %v; want Both", deep.Presence())
	}
}

func TestMergeIsAdditive(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothF("f.txt"))
	f.trees[diffmodel.Left].ApplyListing(f.trees[diffmodel.Left].Root, []diffmodel.ListedEntry{{Name: "f.txt", Type: diffmodel.File}}, nil)
	ApplyCompareResult(f.find("f.txt"), diffmodel.Content, diffmodel.Differs, nil)

	// Merging again must not replace the row and lose its verdict.
	if added := Merge(f.root); len(added) != 0 {
		t.Fatalf("Merge added %d rows on a second pass; want 0", len(added))
	}
	if got := f.find("f.txt"); got.Result != diffmodel.Differs || got.Level != diffmodel.Content {
		t.Fatalf("f.txt = %v at %v after re-merging; want its verdict kept", got.Result, got.Level)
	}
}

func TestApplyCompareResultMonotonic(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothF("f.txt"))
	n := f.find("f.txt")

	ApplyCompareResult(n, diffmodel.Content, diffmodel.Differs, nil)
	// A shallower, stale result arriving afterward must not downgrade the
	// display (SPEC.md §5.3).
	ApplyCompareResult(n, diffmodel.SizeMtime, diffmodel.Same, nil)

	if n.Level != diffmodel.Content || n.Result != diffmodel.Differs {
		t.Fatalf("Level=%v Result=%v; want Content/Differs to survive the shallower re-trigger", n.Level, n.Result)
	}
}

func TestRollupPropagatesUpwardOnDifference(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothD("a"))
	a := f.find("a")
	f.list(a, bothD("b"))
	b := f.find("a/b")
	f.list(b, bothF("f.txt"))

	if f.root.Result != diffmodel.Unknown {
		t.Fatalf("root.Result = %v before any compare; want Unknown", f.root.Result)
	}

	ApplyCompareResult(f.find("a/b/f.txt"), diffmodel.Content, diffmodel.Differs, nil)

	for _, n := range []*Node{f.find("a/b/f.txt"), b, a, f.root} {
		if n.Result != diffmodel.Differs {
			t.Fatalf("%q Result = %v; want Differs to propagate all the way up", n.PairRel, n.Result)
		}
	}
}

func TestRollupTreatsOneSidedChildAsDiffers(t *testing.T) {
	f := newFixture()
	f.list(f.root, leftF("onlyleft.txt"))

	if f.root.Result != diffmodel.Differs {
		t.Fatalf("root.Result = %v; want Differs for a one-sided child, even with no compare run", f.root.Result)
	}
}

func TestRollupCleanWhenAllSame(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothF("a.txt"), bothF("b.txt"))
	for _, c := range f.root.Children {
		ApplyCompareResult(c, diffmodel.Content, diffmodel.Same, nil)
	}
	if f.root.Result != diffmodel.Same {
		t.Fatalf("root.Result = %v; want Same when every compared child is Same", f.root.Result)
	}
}

func TestRollupSameWhenEmptyOnBothSides(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothD("empty"))
	empty := f.find("empty")
	f.list(empty)

	if empty.Result != diffmodel.Same {
		t.Fatalf("empty.Result = %v; want Same for a directory listed on both sides with no children", empty.Result)
	}
	if f.root.Result != diffmodel.Same {
		t.Fatalf("root.Result = %v; want Same to propagate up through an empty-both-sides child", f.root.Result)
	}
}

// TestRollupUncomparedOutranksSame guards against a vacuously-Same empty
// subdirectory dragging an otherwise-uncompared parent down to Same: an
// empty dir sibling contributes nothing to say about the uncompared
// sibling, so the parent must stay Unknown ("not compared") rather than
// reporting Same before the other child has been checked at all.
func TestRollupUncomparedOutranksSame(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothD("empty"), bothF("f.txt"))
	f.list(f.find("empty"))

	if got := f.find("empty").Result; got != diffmodel.Same {
		t.Fatalf("empty.Result = %v; want Same for a directory listed on both sides with no children", got)
	}
	if f.root.Result != diffmodel.Unknown {
		t.Fatalf("root.Result = %v; want Unknown while f.txt hasn't been compared yet, even though the empty sibling is Same", f.root.Result)
	}
}

func TestRollupListErrIsError(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothD("a"))
	a := f.find("a")
	f.listErr(a, errPermission)

	if a.Result != diffmodel.CompareError {
		t.Fatalf("a.Result = %v; want CompareError when the directory itself failed to list", a.Result)
	}
}

func TestRollupLevelUniformAcrossChildren(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothF("a.txt"), bothF("b.txt"))
	for _, c := range f.root.Children {
		ApplyCompareResult(c, diffmodel.Content, diffmodel.Same, nil)
	}
	if f.root.Level != diffmodel.Content || f.root.LevelMixed {
		t.Fatalf("root.Level=%v LevelMixed=%v; want Content/false when every child was compared at the same level", f.root.Level, f.root.LevelMixed)
	}
}

func TestRollupLevelMixedAcrossChildren(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothF("a.txt"), bothF("b.txt"))
	ApplyCompareResult(f.find("a.txt"), diffmodel.SizeMtime, diffmodel.Same, nil)
	ApplyCompareResult(f.find("b.txt"), diffmodel.Content, diffmodel.Same, nil)

	if !f.root.LevelMixed {
		t.Fatal("root.LevelMixed = false; want true when children were compared at different levels")
	}
	if f.root.Level != diffmodel.Content {
		t.Fatalf("root.Level = %v; want Content, the deepest level seen", f.root.Level)
	}
}

func TestRollupLevelIgnoresUncomparedAndOneSidedChildren(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothF("a.txt"), bothF("b.txt"), leftF("onlyleft.txt"))
	ApplyCompareResult(f.find("a.txt"), diffmodel.SizeMtime, diffmodel.Same, nil)
	// b.txt is left NotCompared, and onlyleft.txt can never be compared.

	if f.root.LevelMixed {
		t.Fatal("root.LevelMixed = true; want false — an uncompared or one-sided child shouldn't count as a disagreement")
	}
	if f.root.Level != diffmodel.SizeMtime {
		t.Fatalf("root.Level = %v; want SizeMtime, the only level actually observed", f.root.Level)
	}
}

func TestRollupLevelPropagatesFromNestedDirectory(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothD("a"))
	a := f.find("a")
	f.list(a, bothF("x.txt"), bothF("y.txt"))
	ApplyCompareResult(f.find("a/x.txt"), diffmodel.SizeMtime, diffmodel.Same, nil)
	ApplyCompareResult(f.find("a/y.txt"), diffmodel.Content, diffmodel.Same, nil)

	if !a.LevelMixed {
		t.Fatal("a.LevelMixed = false; want true")
	}
	if !f.root.LevelMixed {
		t.Fatal("root.LevelMixed = false; want the mixed flag to propagate up through a directory child")
	}
}

// TestSideTotalsComeStraightFromTheSideTrees covers what moved out of
// this package: a row's per-side counts are its side node's own, not
// something the pairing aggregates.
func TestSideTotalsComeStraightFromTheSideTrees(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothD("sub"), leftD("gone"), rightF("added.txt"), bothL("link"))
	f.list(f.find("sub"), bothF("deep.txt"))

	left, right := f.root.SideTotals()
	if left.Dirs != 2 || right.Dirs != 1 {
		t.Errorf("dirs = %d left / %d right; want 2/1 — the left-only directory counts on the left alone", left.Dirs, right.Dirs)
	}
	if left.Files != 1 || right.Files != 2 {
		t.Errorf("files = %d left / %d right; want 1/2 — the nested file counts on both sides, the right-only one on the right", left.Files, right.Files)
	}
	if left.Symlinks != 1 || right.Symlinks != 1 {
		t.Errorf("symlinks = %d left / %d right; want 1/1", left.Symlinks, right.Symlinks)
	}

	// A one-sided directory reports nothing at all on its missing side —
	// callers tell that apart from an empty subtree by its Presence.
	gone := f.find("gone")
	if _, goneRight := gone.SideTotals(); goneRight != (sidetree.Totals{}) {
		t.Errorf("left-only directory's right totals = %+v; want the zero value", goneRight)
	}
	if gone.Presence() != diffmodel.LeftOnly {
		t.Errorf("gone.Presence = %v; want LeftOnly", gone.Presence())
	}
}

var errPermission = &permErr{}

type permErr struct{}

func (*permErr) Error() string { return "permission denied" }
