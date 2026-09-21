package ui

import (
	"strings"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/pairtree"
)

// FilterStatus is one row-status filter option (SPEC.md §4.7): the
// filter's options are exactly diffmodel's row statuses, under the names
// the popup uses for them. Keeping them the same type is what lets a
// selection be tested directly against a row's own status and against
// the per-status descendant tallies pairtree.Node keeps, with no mapping in
// between that could drift from either.
type FilterStatus = diffmodel.RowStatus

const (
	FilterLeftOnly  = diffmodel.RowLeftOnly
	FilterRightOnly = diffmodel.RowRightOnly
	FilterEqual     = diffmodel.RowEqual
	FilterDifferent = diffmodel.RowDifferent
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
func (m Model) visibleChildren() []*pairtree.Node {
	if m.atRootParent {
		// The root pair is the only row at that level and the only way
		// back down into the tree, so the filter never applies to it — the
		// same reasoning as the one-sided navigation placeholder (SPEC.md
		// §4.3), which filtering also leaves alone.
		return []*pairtree.Node{m.cursorDir}
	}
	return filterChildren(m.cursorDir.Children, m.filter)
}

func filterChildren(children []*pairtree.Node, filter FilterSet) []*pairtree.Node {
	if filter.isAll() {
		return children
	}
	out := make([]*pairtree.Node, 0, len(children))
	for _, c := range children {
		if matchesFilter(c, filter) || hasMatchingDescendant(c, filter) {
			out = append(out, c)
		}
	}
	return out
}

// matchesFilter reports whether n itself (not any descendant) satisfies
// any status in filter. Which status a row has is diffmodel's call, not
// this package's (see diffmodel.ClassifyRow): notably, a directory never
// has the Equal or Different status, so it can only surface under those
// filters via hasMatchingDescendant.
func matchesFilter(n *pairtree.Node, filter FilterSet) bool {
	s := n.RowStatus()
	return s != diffmodel.RowNone && filter[s]
}

// hasMatchingDescendant reports whether n's already-known subtree holds
// a row matching any selected status (SPEC.md §4.7's "dimmed ancestor"
// case), by reading the tallies pairtree.Node maintains rather than walking
// the subtree: this runs for every visible row on every render, so a
// walk here costs a full traversal of the tree several times a second
// even when nothing is happening.
//
// The tallies only cover what's been listed so far — a directory not yet
// listed has no children to count — which is why the filtered view
// re-evaluates live as background listing and comparison results stream
// in.
func hasMatchingDescendant(n *pairtree.Node, filter FilterSet) bool {
	for _, f := range allFilters {
		if filter[f] && n.DescendantsWithStatus(f) > 0 {
			return true
		}
	}
	return false
}
