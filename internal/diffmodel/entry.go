// Package diffmodel holds the core data types shared between scanning,
// comparison, and the UI: entry types, presence/comparison status, and the
// comparison levels described in SPEC.md.
package diffmodel

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

// Presence indicates which side(s) an entry exists on.
type Presence int

const (
	Both Presence = iota
	LeftOnly
	RightOnly
)

// CompareLevel is how thoroughly two sides of an entry have been checked.
// Existence itself is not a level (SPEC.md §5.1) — it's implied once an
// entry's Presence is known from listing.
type CompareLevel int

const (
	NotCompared CompareLevel = iota
	Size
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

// Entry is one row in a directory listing pane: a name matched (or
// unmatched) between the left and right trees.
type Entry struct {
	Name     string
	Type     EntryType
	Presence Presence

	Level  CompareLevel
	Result CompareResult

	// Rollup summarizes descendant status for Dir entries (SPEC.md §3.3).
	// It is the zero value (Unknown) until at least one descendant has
	// been examined.
	Rollup CompareResult
}
