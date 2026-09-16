package ui

import (
	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/tree"
)

// FilterStatus is the persistent row-status filter setting (SPEC.md §4.7),
// alongside compareLevel and recursive.
type FilterStatus int

const (
	FilterAll FilterStatus = iota
	FilterLeftOnly
	FilterRightOnly
	FilterEqual
	FilterDifferent
)

// allFilters is the popup's fixed option order.
var allFilters = []FilterStatus{FilterAll, FilterLeftOnly, FilterRightOnly, FilterEqual, FilterDifferent}

func filterLabel(f FilterStatus) string {
	switch f {
	case FilterLeftOnly:
		return "left only"
	case FilterRightOnly:
		return "right only"
	case FilterEqual:
		return "equal"
	case FilterDifferent:
		return "different"
	default:
		return "all"
	}
}

func filterIndex(f FilterStatus) int {
	for i, v := range allFilters {
		if v == f {
			return i
		}
	}
	return 0
}

// visibleChildren returns m.cursorDir's children that pass the active
// filter (SPEC.md §4.7) — every child when the filter is FilterAll.
func (m Model) visibleChildren() []*tree.Node {
	return filterChildren(m.cursorDir.Children, m.filter)
}

func filterChildren(children []*tree.Node, filter FilterStatus) []*tree.Node {
	if filter == FilterAll {
		return children
	}
	out := make([]*tree.Node, 0, len(children))
	for _, c := range children {
		if matchesFilter(c, filter) || hasMatchingDescendant(c, filter) {
			out = append(out, c)
		}
	}
	return out
}

// matchesFilter reports whether n itself (not any descendant) satisfies
// filter. For a directory, Presence is its own — independent of
// rollup — while Result is the §3.3 rollup, so a directory matches
// Equal/Different exactly when its rolled-up status already resolves that
// way (e.g. an entirely clean subtree matches Equal directly).
func matchesFilter(n *tree.Node, filter FilterStatus) bool {
	switch filter {
	case FilterLeftOnly:
		return n.Presence == diffmodel.LeftOnly
	case FilterRightOnly:
		return n.Presence == diffmodel.RightOnly
	case FilterEqual:
		return n.Presence == diffmodel.Both && n.Result == diffmodel.Same
	case FilterDifferent:
		return n.Presence == diffmodel.Both && (n.Result == diffmodel.Differs || n.Result == diffmodel.CompareError)
	default:
		return true
	}
}

// hasMatchingDescendant recursively searches n's already-known subtree for
// a matching row (SPEC.md §4.7's "dimmed ancestor" case). It only sees
// what's been listed so far — a directory not yet listed has no Children —
// which is why the filtered view re-evaluates live as background listing
// and comparison results stream in.
func hasMatchingDescendant(n *tree.Node, filter FilterStatus) bool {
	for _, c := range n.Children {
		if matchesFilter(c, filter) || hasMatchingDescendant(c, filter) {
			return true
		}
	}
	return false
}
