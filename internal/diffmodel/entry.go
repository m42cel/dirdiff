// Package diffmodel holds the core data types shared between scanning,
// comparison, and the UI: entry types, presence/comparison status, and the
// comparison levels described in SPEC.md. It has no dependency on
// filesystem I/O or the UI framework, so scanning and tree-state code can
// both build on it without coupling to each other.
package diffmodel

import "time"

// EntryType distinguishes what kind of filesystem object a row represents.
// Files, directories, and symlinks are matched independently (see
// SPEC.md §3.1) so a name colliding across types on the two sides never
// produces a merged row.
type EntryType int

const (
	File EntryType = iota
	Dir
	Symlink
)

// Side names one of the two compared trees. Listing and metadata are
// per-side work — each tree is read on its own, and a subtree may be
// paired with any other — so every job and result below the pairing
// layer carries the side it belongs to.
type Side int

const (
	Left Side = iota
	Right
)

// Sides is every Side, for callers that do the same thing to both.
var Sides = [...]Side{Left, Right}

// Presence indicates which side(s) an entry exists on.
type Presence int

const (
	Both Presence = iota
	LeftOnly
	RightOnly
)

// CompareLevel is how thoroughly two sides of an entry have been checked.
// Existence itself is not a level (SPEC.md §5.1) — it's implied once an
// entry's Presence is known from listing. Values are ordered shallowest
// to deepest so callers can compare levels with plain <, <=, etc.
//
// There are only two triggered levels: SizeMtime (size AND mtime checked
// together as one verdict — a file only counts as "same" at this level if
// both match) and Checksum.
type CompareLevel int

const (
	NotCompared CompareLevel = iota
	SizeMtime
	Checksum
)

// CompareResult is the outcome of the deepest CompareLevel run so far.
// Re-running a shallower level never downgrades a deeper result
// (SPEC.md §5.3).
type CompareResult int

const (
	Unknown CompareResult = iota
	Same
	Differs
	CompareError
)

// RowStatus is the single status a row is filtered by (SPEC.md §4.7):
// which side it exists on, or — for a matched file or symlink — how its
// two sides compared. It's derived from a row's type/presence/result
// rather than stored, so there is exactly one definition of what "equal"
// or "different" means for a row, shared by the UI's filter and by the
// per-subtree tallies package tree keeps.
type RowStatus int

const (
	RowLeftOnly RowStatus = iota
	RowRightOnly
	RowEqual
	RowDifferent

	// RowStatusCount is the number of real statuses, for sizing arrays
	// indexed by a RowStatus.
	RowStatusCount

	// RowNone is not one of those: it's what a row with no status at all
	// classifies as — a matched entry not yet compared, or a directory,
	// whose Result is a rollup of its subtree rather than a statement
	// about the directory itself.
	RowNone RowStatus = -1
)

// ClassifyRow reports which status a row has. A one-sided entry is
// classified by presence alone, whatever its type — a directory present
// on only one side is a Left-only/Right-only row just as a file would
// be. Equal/Different, by contrast, only ever apply to a file or
// symlink: a directory's CompareResult is the SPEC.md §3.3 rollup of
// everything beneath it, not a property of the directory, so a directory
// never classifies as Equal or Different.
func ClassifyRow(t EntryType, p Presence, r CompareResult) RowStatus {
	switch p {
	case LeftOnly:
		return RowLeftOnly
	case RightOnly:
		return RowRightOnly
	}
	if t == Dir {
		return RowNone
	}
	switch r {
	case Same:
		return RowEqual
	case Differs, CompareError:
		return RowDifferent
	}
	return RowNone
}

// ListedEntry describes one entry discovered while listing a directory
// on one side: what readdir reports about it and nothing more. Matching
// the two sides' entries by (name, type) per SPEC.md §3.1 happens a
// layer up, over the two side trees, so the same listing can serve any
// number of pairings.
type ListedEntry struct {
	Name string
	Type EntryType
}

// EntryLess is the single definition of the order entries are held and
// rendered in (SPEC.md §4.1): directories before everything else, then
// alphabetically by exact byte comparison — no case folding and no
// Unicode normalization, the same rule matching uses (SPEC.md §3.1).
// Every tree that stores entries sorts by this, so a pane's order never
// depends on which layer happened to produce the listing.
func EntryLess(aName string, aType EntryType, bName string, bType EntryType) bool {
	if (aType == Dir) != (bType == Dir) {
		return aType == Dir
	}
	return aName < bName
}

// StatInfo holds size/mtime metadata for both sides of a compared entry,
// populated once a SizeMtime (or deeper) comparison has run.
type StatInfo struct {
	LeftSize, RightSize   int64
	LeftMtime, RightMtime time.Time
}
