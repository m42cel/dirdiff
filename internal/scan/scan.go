// Package scan does the actual filesystem I/O: listing a directory on
// both sides and merging the result, and comparing one entry at a given
// CompareLevel. Every function here is a pure, self-contained operation
// on the two absolute paths it's given — nothing here touches shared
// state, so these are safe to run concurrently from a worker pool.
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

// ListJob asks for the merged listing of RelPath under both roots.
type ListJob struct {
	RelPath           string
	LeftAbs, RightAbs string
}

// ListResult is the outcome of a ListJob. LeftErr/RightErr are set only
// for read failures other than "doesn't exist" (e.g. permission denied) —
// a missing side is not an error, it's how one-sided entries are
// discovered (SPEC.md §4.3).
type ListResult struct {
	RelPath           string
	Children          []diffmodel.ListedChild
	LeftErr, RightErr error
}

// DoList lists job.LeftAbs and job.RightAbs and merges them by (name,
// type) per SPEC.md §3.1, sorted with directories first, then
// alphabetically (SPEC.md §4.1). The two sides are read concurrently, not
// sequentially: on two different physical devices (or even just two
// distant regions of the same one) the job's latency is then bounded by
// the slower side alone, instead of the sum of both.
func DoList(job ListJob) ListResult {
	var right []typedEntry
	var rightErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		right, rightErr = readDirTyped(job.RightAbs)
	}()

	left, leftErr := readDirTyped(job.LeftAbs)
	wg.Wait()

	type key struct {
		name string
		typ  diffmodel.EntryType
	}
	presence := map[key]diffmodel.Presence{}
	for _, e := range left {
		presence[key{e.name, e.typ}] = diffmodel.LeftOnly
	}
	for _, e := range right {
		k := key{e.name, e.typ}
		if _, ok := presence[k]; ok {
			presence[k] = diffmodel.Both
		} else {
			presence[k] = diffmodel.RightOnly
		}
	}

	children := make([]diffmodel.ListedChild, 0, len(presence))
	for k, p := range presence {
		children = append(children, diffmodel.ListedChild{Name: k.name, Type: k.typ, Presence: p})
	}
	sort.Slice(children, func(i, j int) bool {
		if (children[i].Type == diffmodel.Dir) != (children[j].Type == diffmodel.Dir) {
			return children[i].Type == diffmodel.Dir
		}
		return children[i].Name < children[j].Name
	})

	return ListResult{RelPath: job.RelPath, Children: children, LeftErr: leftErr, RightErr: rightErr}
}

type typedEntry struct {
	name string
	typ  diffmodel.EntryType
}

// readDirTyped lists dir, classifying each entry as File, Dir, or
// Symlink without following symlinks (SPEC.md §7). A missing directory
// is not an error — it's the normal "doesn't exist on this side" case.
func readDirTyped(dir string) ([]typedEntry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]typedEntry, 0, len(des))
	for _, de := range des {
		t := diffmodel.File
		if de.Type()&os.ModeSymlink != 0 {
			t = diffmodel.Symlink
		} else if de.IsDir() {
			t = diffmodel.Dir
		}
		out = append(out, typedEntry{name: de.Name(), typ: t})
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
