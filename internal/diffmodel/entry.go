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

// ListedChild describes one merged entry discovered while listing a
// directory on both sides: a name matched (or unmatched) by (name, type)
// per SPEC.md §3.1.
type ListedChild struct {
	Name     string
	Type     EntryType
	Presence Presence
}

// StatInfo holds size/mtime metadata for both sides of a compared entry,
// populated once a Size or SizeMtime (or deeper) comparison has run.
type StatInfo struct {
	LeftSize, RightSize   int64
	LeftMtime, RightMtime time.Time
}
