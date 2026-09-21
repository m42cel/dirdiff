package ui

import (
	"testing"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/pairtree"
	"github.com/m42cel/dirdiff/internal/sidetree"
)

func setOf(fs ...FilterStatus) FilterSet {
	s := make(FilterSet, len(fs))
	for _, f := range fs {
		s[f] = true
	}
	return s
}

// row builds a pair node standing on whichever sides presence says it
// exists on — presence is derived from those two pointers, not stored.
func row(name string, typ diffmodel.EntryType, presence diffmodel.Presence, result diffmodel.CompareResult) *pairtree.Node {
	n := &pairtree.Node{Type: typ, RelPath: name, Result: result}
	if presence != diffmodel.RightOnly {
		n.Left = &sidetree.Node{Name: name, Type: typ, RelPath: name}
	}
	if presence != diffmodel.LeftOnly {
		n.Right = &sidetree.Node{Name: name, Type: typ, RelPath: name}
	}
	return n
}

func file(name string, presence diffmodel.Presence, result diffmodel.CompareResult) *pairtree.Node {
	return row(name, diffmodel.File, presence, result)
}

// dir links its children with pairtree.AddChild rather than assigning
// Children directly, since that's what keeps the per-status descendant
// tallies hasMatchingDescendant reads in sync — a hand-linked subtree
// would report as empty. Children must already carry their final
// status, as they do here (nothing re-compares them afterwards).
func dir(name string, presence diffmodel.Presence, result diffmodel.CompareResult, children ...*pairtree.Node) *pairtree.Node {
	d := row(name, diffmodel.Dir, presence, result)
	for _, c := range children {
		pairtree.AddChild(d, c)
	}
	return d
}

// A directory that rolls up to Differs purely because it contains a
// left-only child must not match FilterDifferent directly — only the
// child's own presence-based filter should surface it.
func TestMatchesFilter_DirectoryNeverMatchesEqualOrDifferentDirectly(t *testing.T) {
	leftOnlyChild := file("only-here", diffmodel.LeftOnly, diffmodel.Unknown)
	d := dir("sub", diffmodel.Both, diffmodel.Differs, leftOnlyChild)

	if matchesFilter(d, setOf(FilterDifferent)) {
		t.Fatal("directory rolled up to Differs must not match FilterDifferent directly")
	}
	if matchesFilter(d, setOf(FilterEqual)) {
		t.Fatal("directory must not match FilterEqual directly regardless of rollup")
	}
}

func TestFilterChildren_DirectoryVisibleOnlyUnderMatchingFilter(t *testing.T) {
	leftOnlyChild := file("only-here", diffmodel.LeftOnly, diffmodel.Unknown)
	d := dir("sub", diffmodel.Both, diffmodel.Differs, leftOnlyChild)
	siblings := []*pairtree.Node{d}

	if got := filterChildren(siblings, setOf(FilterDifferent)); len(got) != 0 {
		t.Fatalf("dir with only a left-only child must be hidden under FilterDifferent, got %d rows", len(got))
	}
	if got := filterChildren(siblings, setOf(FilterEqual)); len(got) != 0 {
		t.Fatalf("dir with only a left-only child must be hidden under FilterEqual, got %d rows", len(got))
	}
	got := filterChildren(siblings, setOf(FilterLeftOnly))
	if len(got) != 1 || got[0] != d {
		t.Fatalf("dir with a left-only descendant must show (dimmed) under FilterLeftOnly, got %v", got)
	}
}

func TestFilterChildren_EntirelyEqualSubtreeSurfacesViaDescendant(t *testing.T) {
	equalFile := file("a", diffmodel.Both, diffmodel.Same)
	d := dir("clean", diffmodel.Both, diffmodel.Same, equalFile)
	siblings := []*pairtree.Node{d}

	got := filterChildren(siblings, setOf(FilterEqual))
	if len(got) != 1 || got[0] != d {
		t.Fatalf("clean subtree's directory must still show under FilterEqual via its equal child, got %v", got)
	}
	if !hasMatchingDescendant(d, setOf(FilterEqual)) {
		t.Fatal("hasMatchingDescendant should find the equal child, not rely on the directory's own rollup")
	}
}

func TestMatchesFilter_MultiSelectIsOrCombined(t *testing.T) {
	leftOnly := file("a", diffmodel.LeftOnly, diffmodel.Unknown)
	equal := file("b", diffmodel.Both, diffmodel.Same)
	differs := file("c", diffmodel.Both, diffmodel.Differs)

	sel := setOf(FilterLeftOnly, FilterEqual)
	if !matchesFilter(leftOnly, sel) {
		t.Error("left-only row should match a set containing FilterLeftOnly")
	}
	if !matchesFilter(equal, sel) {
		t.Error("equal row should match a set containing FilterEqual")
	}
	if matchesFilter(differs, sel) {
		t.Error("differing row should not match a set that selects neither Different nor its own presence")
	}
}

func TestMatchesFilter_EmptySetMatchesNothing(t *testing.T) {
	nodes := []*pairtree.Node{
		file("a", diffmodel.LeftOnly, diffmodel.Unknown),
		file("b", diffmodel.RightOnly, diffmodel.Unknown),
		file("c", diffmodel.Both, diffmodel.Same),
		file("d", diffmodel.Both, diffmodel.Differs),
	}
	empty := FilterSet{}
	for _, n := range nodes {
		if matchesFilter(n, empty) {
			t.Errorf("node %s unexpectedly matched an empty filter set", n.Name())
		}
	}
	if got := filterChildren(nodes, empty); len(got) != 0 {
		t.Fatalf("filterChildren with an empty set should hide everything, got %d rows", len(got))
	}
}

func TestFilterSetLabel(t *testing.T) {
	if got := filterSetLabel(defaultFilterSet()); got != "all" {
		t.Errorf("all four selected should summarize as \"all\", got %q", got)
	}
	if got := filterSetLabel(FilterSet{}); got != "none" {
		t.Errorf("nothing selected should summarize as \"none\", got %q", got)
	}
	if got := filterSetLabel(setOf(FilterLeftOnly, FilterEqual)); got != "left only, equal" {
		t.Errorf("partial selection should join labels in allFilters order, got %q", got)
	}
}
