// Package scan does the actual filesystem I/O: listing one side's
// directory, and comparing one entry at a given CompareLevel. Every
// function here is a pure, self-contained operation on the absolute
// paths it's given — nothing here touches shared state, so these are
// safe to run concurrently from a worker pool.
package scan

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
)

// ListJob asks for RelPath to be listed on one side. Listing is
// per-side work: the two trees are read independently and matched
// afterwards, so a directory paired with one at an entirely different
// path needs no listing of its own.
type ListJob struct {
	Side    diffmodel.Side
	RelPath string
	Abs     string
}

// ListResult is the outcome of a ListJob. Err is set only for a read
// failure other than "doesn't exist" (e.g. permission denied) — a
// missing directory is not an error, it's how one-sided entries are
// discovered (SPEC.md §4.3).
type ListResult struct {
	Side    diffmodel.Side
	RelPath string
	Entries []diffmodel.ListedEntry
	Err     error
}

// DoList lists job.Abs in diffmodel.EntryLess order (SPEC.md §4.1) —
// the order the panes render in, so a listing is already in it before
// any matching happens.
func DoList(job ListJob) ListResult {
	entries, err := readDirTyped(job.Abs)
	sort.Slice(entries, func(i, j int) bool {
		return diffmodel.EntryLess(entries[i].Name, entries[i].Type, entries[j].Name, entries[j].Type)
	})
	return ListResult{Side: job.Side, RelPath: job.RelPath, Entries: entries, Err: err}
}

// readDirTyped lists dir, classifying each entry as File, Dir, or
// Symlink without following symlinks (SPEC.md §7). A missing directory
// is not an error — it's the normal "doesn't exist on this side" case.
func readDirTyped(dir string) ([]diffmodel.ListedEntry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]diffmodel.ListedEntry, 0, len(des))
	for _, de := range des {
		t := diffmodel.File
		if de.Type()&os.ModeSymlink != 0 {
			t = diffmodel.Symlink
		} else if de.IsDir() {
			t = diffmodel.Dir
		}
		out = append(out, diffmodel.ListedEntry{Name: de.Name(), Type: t})
	}
	return out, nil
}

// CompareJob asks for entry RelPath (present on both sides) to be
// compared at Level.
type CompareJob struct {
	RelPath           string
	LeftAbs, RightAbs string
	Type              diffmodel.EntryType
	Level             diffmodel.CompareLevel
}

// CompareOutcome is the result of a CompareJob.
type CompareOutcome struct {
	RelPath string
	Level   diffmodel.CompareLevel
	Result  diffmodel.CompareResult
	Err     error
	Stat    *diffmodel.StatInfo // non-nil when size/mtime were read
}

// DoCompare runs job and returns its outcome. Symlinks are never
// followed — any level compares the two link targets as strings
// (SPEC.md §7), which is cheap enough to always do in full regardless of
// the requested level.
func DoCompare(job CompareJob) CompareOutcome {
	if job.Type == diffmodel.Symlink {
		return compareSymlink(job)
	}

	leftInfo, err := os.Lstat(job.LeftAbs)
	if err != nil {
		return CompareOutcome{RelPath: job.RelPath, Level: job.Level, Result: diffmodel.CompareError, Err: err}
	}
	rightInfo, err := os.Lstat(job.RightAbs)
	if err != nil {
		return CompareOutcome{RelPath: job.RelPath, Level: job.Level, Result: diffmodel.CompareError, Err: err}
	}

	stat := &diffmodel.StatInfo{
		LeftSize: leftInfo.Size(), RightSize: rightInfo.Size(),
		LeftMtime: leftInfo.ModTime(), RightMtime: rightInfo.ModTime(),
	}

	switch job.Level {
	case diffmodel.SizeMtime:
		result := diffmodel.Differs
		if leftInfo.Size() == rightInfo.Size() && sameMtime(leftInfo.ModTime(), rightInfo.ModTime()) {
			result = diffmodel.Same
		}
		return CompareOutcome{RelPath: job.RelPath, Level: job.Level, Result: result, Stat: stat}

	case diffmodel.Checksum:
		equal, err := filesEqual(job.LeftAbs, job.RightAbs)
		if err != nil {
			return CompareOutcome{RelPath: job.RelPath, Level: job.Level, Result: diffmodel.CompareError, Err: err, Stat: stat}
		}
		result := diffmodel.Differs
		if equal {
			result = diffmodel.Same
		}
		return CompareOutcome{RelPath: job.RelPath, Level: job.Level, Result: result, Stat: stat}

	default:
		return CompareOutcome{RelPath: job.RelPath, Level: job.Level, Result: diffmodel.Unknown, Stat: stat}
	}
}

func sameMtime(a, b time.Time) bool { return a.Equal(b) }

func compareSymlink(job CompareJob) CompareOutcome {
	left, err := os.Readlink(job.LeftAbs)
	if err != nil {
		return CompareOutcome{RelPath: job.RelPath, Level: diffmodel.Checksum, Result: diffmodel.CompareError, Err: err}
	}
	right, err := os.Readlink(job.RightAbs)
	if err != nil {
		return CompareOutcome{RelPath: job.RelPath, Level: diffmodel.Checksum, Result: diffmodel.CompareError, Err: err}
	}
	result := diffmodel.Differs
	if left == right {
		result = diffmodel.Same
	}
	return CompareOutcome{RelPath: job.RelPath, Level: diffmodel.Checksum, Result: result}
}

// filesEqual does a streaming, chunked byte-for-byte comparison, short
// circuiting as soon as a differing chunk is found (SPEC.md §2.1). Each
// chunk pair is read concurrently, not sequentially — on two different
// physical devices (or even just two distant regions of the same one)
// this bounds a chunk's read latency by the slower side alone rather
// than the sum of both, the same reasoning as DoList's concurrent
// left/right reads.
func filesEqual(leftPath, rightPath string) (bool, error) {
	lf, err := os.Open(leftPath)
	if err != nil {
		return false, err
	}
	defer lf.Close()
	rf, err := os.Open(rightPath)
	if err != nil {
		return false, err
	}
	defer rf.Close()

	const chunkSize = 64 * 1024
	lb := make([]byte, chunkSize)
	rb := make([]byte, chunkSize)

	for {
		var rn int
		var rerr error
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			rn, rerr = io.ReadFull(rf, rb)
		}()

		ln, lerr := io.ReadFull(lf, lb)
		wg.Wait()

		if ln != rn || !bytes.Equal(lb[:ln], rb[:rn]) {
			return false, nil
		}

		lDone := lerr == io.EOF || lerr == io.ErrUnexpectedEOF
		rDone := rerr == io.EOF || rerr == io.ErrUnexpectedEOF
		if lerr != nil && !lDone {
			return false, lerr
		}
		if rerr != nil && !rDone {
			return false, rerr
		}
		if lDone != rDone {
			return false, nil
		}
		if lDone {
			return true, nil
		}
	}
}

// AbsPath joins root and relPath (relPath may be "" for the root itself).
func AbsPath(root, relPath string) string {
	if relPath == "" {
		return root
	}
	return filepath.Join(root, relPath)
}
