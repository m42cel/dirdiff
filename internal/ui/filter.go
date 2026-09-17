package ui

import (
	"strings"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/tree"
)

// FilterStatus is one row-status filter option (SPEC.md §4.7).
type FilterStatus int

const (
	FilterLeftOnly FilterStatus = iota
	FilterRightOnly
	FilterEqual
	FilterDifferent
)

// allFilters is the popup's fixed option order.
var allFilters = []FilterStatus{FilterLeftOnly, FilterRightOnly, FilterEqual, FilterDifferent}

// FilterSet is the persistent, multi-select row-status filter (SPEC.md
// §4.7): a row is visible if it matches any status in the set (OR
// combined — the four statuses are mutually exclusive per row, so AND
// would always be empty for more than one selection). Every status
// selected is the unfiltered, "show everything" state.
type FilterSet map[FilterStatus]bool

// defaultFilterSet is dirdiff's starting filter: every status selected,
// i.e. unfiltered.
func defaultFilterSet() FilterSet {
	s := make(FilterSet, len(allFilters))
	for _, f := range allFilters {
		s[f] = true
	}
	return s
}

func cloneFilterSet(s FilterSet) FilterSet {
	out := make(FilterSet, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

func (s FilterSet) isAll() bool {
	for _, f := range allFilters {
		if !s[f] {
			return false
		}
	}
	return true
}

func (s FilterSet) isEmpty() bool {
	for _, f := range allFilters {
		if s[f] {
			return false
		}
	}
	return true
}

func filterLabel(f FilterStatus) string {
	switch f {
	case FilterLeftOnly:
		return "left only"
	case FilterRightOnly:
		return "right only"
	case FilterEqual:
		return "equal"
	default:
		return "different"
	}
}

// filterSetLabel summarizes s for the status bar: "all" when nothing is
// excluded (the common, unfiltered case), "none" when nothing is
// selected, otherwise the selected labels joined by ", ".
func filterSetLabel(s FilterSet) string {
	if s.isAll() {
		return "all"
	}
	var labels []string
	for _, f := range allFilters {
		if s[f] {
			labels = append(labels, filterLabel(f))
		}
	}
	if len(labels) == 0 {
		return "none"
	}
	return strings.Join(labels, ", ")
}

// visibleChildren returns m.cursorDir's children that pass the active
// filter set (SPEC.md §4.7) — every child when the set is "all".
func (m Model) visibleChildren() []*tree.Node {
	return filterChildren(m.cursorDir.Children, m.filter)
}

func filterChildren(children []*tree.Node, filter FilterSet) []*tree.Node {
	if filter.isAll() {
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
// any status in filter. Left-only/Right-only match a row's own
// Presence — this applies to directories too, independent of anything
// below them. Equal/Different only ever match a non-directory row (a
// file or symlink) directly, on its own Result: a directory's Result is
// the §3.3 rollup of what's below it, not a property of the directory
// itself, so it never satisfies Equal/Different directly — it can only
// surface via hasMatchingDescendant, same as any other filter it doesn't
// itself match.
func matchesFilter(n *tree.Node, filter FilterSet) bool {
	switch n.Presence {
	case diffmodel.LeftOnly:
		return filter[FilterLeftOnly]
	case diffmodel.RightOnly:
		return filter[FilterRightOnly]
	}
	if n.IsDir() {
		return false
	}
	switch n.Result {
	case diffmodel.Same:
		return filter[FilterEqual]
	case diffmodel.Differs, diffmodel.CompareError:
		return filter[FilterDifferent]
	default:
		return false
	}
}

// hasMatchingDescendant recursively searches n's already-known subtree for
// a matching row (SPEC.md §4.7's "dimmed ancestor" case). It only sees
// what's been listed so far — a directory not yet listed has no Children —
// which is why the filtered view re-evaluates live as background listing
// and comparison results stream in.
func hasMatchingDescendant(n *tree.Node, filter FilterSet) bool {
	for _, c := range n.Children {
		if matchesFilter(c, filter) || hasMatchingDescendant(c, filter) {
			return true
		}
	}
	return false
}
