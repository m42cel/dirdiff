package scan

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
)

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDoListReportsNamesAndTypes(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "f.txt"), "x")
	mustMkdir(t, filepath.Join(dir, "sub"))
	if err := os.Symlink("/target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	res := DoList(ListJob{Side: diffmodel.Left, RelPath: "", Abs: dir})
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Side != diffmodel.Left {
		t.Errorf("Side = %v; want Left — a result has to say which tree it came from", res.Side)
	}

	want := map[string]diffmodel.EntryType{
		"f.txt": diffmodel.File,
		"sub":   diffmodel.Dir,
		// A symlink is classified as one without being followed (SPEC.md §7),
		// even when it points at a directory or at nothing at all.
		"link": diffmodel.Symlink,
	}
	got := map[string]diffmodel.EntryType{}
	for _, e := range res.Entries {
		got[e.Name] = e.Type
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %v; want %d of them", res.Entries, len(want))
	}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("%s: Type = %v; want %v", name, got[name], typ)
		}
	}
}

func TestDoListSortsDirsFirstThenAlpha(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"zzz.txt", "aaa.txt"} {
		mustWrite(t, filepath.Join(dir, name), "x")
	}
	for _, name := range []string{"zdir", "adir"} {
		mustMkdir(t, filepath.Join(dir, name))
	}

	res := DoList(ListJob{Abs: dir})
	var names []string
	for _, e := range res.Entries {
		names = append(names, e.Name)
	}
	want := []string{"adir", "zdir", "aaa.txt", "zzz.txt"}
	if len(names) != len(want) {
		t.Fatalf("got %v; want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v; want %v", names, want)
		}
	}
}

func TestDoListMissingDirectoryIsNotAnError(t *testing.T) {
	// A directory that exists on one side and not the other is the normal
	// one-sided case, not a failure (SPEC.md §4.3).
	res := DoList(ListJob{Abs: filepath.Join(t.TempDir(), "does-not-exist")})
	if res.Err != nil {
		t.Fatalf("Err = %v; want nil (missing dir is not an error)", res.Err)
	}
	if len(res.Entries) != 0 {
		t.Fatalf("Entries = %v; want none", res.Entries)
	}
}

// A size mismatch is a conclusive content verdict, so DoCompare answers
// it without opening either file. Whoever enqueues the job normally
// knows both sizes already and never creates it (SPEC.md §5.1); this is
// the fallback for a file that changed since it was statted.
func TestDoCompareSizeMismatchNeedsNoRead(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	mustWrite(t, filepath.Join(left, "f"), "hello")
	mustWrite(t, filepath.Join(right, "f"), "hello!!!")
	// Unreadable on both sides: opening either one would fail outright,
	// so a CompareError here would prove the precheck didn't happen.
	for _, dir := range []string{left, right} {
		if err := os.Chmod(filepath.Join(dir, "f"), 0o000); err != nil {
			t.Fatal(err)
		}
	}

	out := DoCompare(CompareJob{
		LeftAbs: filepath.Join(left, "f"), RightAbs: filepath.Join(right, "f"),
		Type: diffmodel.File, Level: diffmodel.Checksum,
	})
	if out.Result != diffmodel.Differs {
		t.Fatalf("Result = %v (err %v); want Differs decided from the sizes alone", out.Result, out.Err)
	}
}

func TestDoStatReadsSizeAndMtime(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "f"), "hello")
	when := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "f"), when, when); err != nil {
		t.Fatal(err)
	}

	out := DoStat(StatJob{Side: diffmodel.Right, RelPath: "f", Abs: filepath.Join(dir, "f"), Type: diffmodel.File})
	if out.Err != nil {
		t.Fatalf("Err = %v; want nil", out.Err)
	}
	if out.Side != diffmodel.Right || out.RelPath != "f" {
		t.Errorf("result = %v %q; want Right f — a result has to say what it's about", out.Side, out.RelPath)
	}
	if out.Size != 5 {
		t.Errorf("Size = %d; want 5", out.Size)
	}
	if !out.Mtime.Equal(when) {
		t.Errorf("Mtime = %v; want %v", out.Mtime, when)
	}
	if out.LinkTarget != "" {
		t.Errorf("LinkTarget = %q; want empty for a regular file", out.LinkTarget)
	}
}

// A symlink is compared by its target string alone (SPEC.md §7), so the
// stat that measures it reads that target too — and never follows it,
// which is why a link to nowhere still stats fine.
func TestDoStatReadsSymlinkTargetWithoutFollowing(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/nowhere/at/all", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	out := DoStat(StatJob{RelPath: "link", Abs: filepath.Join(dir, "link"), Type: diffmodel.Symlink})
	if out.Err != nil {
		t.Fatalf("Err = %v; want nil — a dangling link is still a link", out.Err)
	}
	if out.LinkTarget != "/nowhere/at/all" {
		t.Errorf("LinkTarget = %q; want the target string", out.LinkTarget)
	}
}

func TestDoStatMissingEntryIsAnError(t *testing.T) {
	// Unlike a listing, where a missing directory is the normal one-sided
	// case: this entry was seen by a listing of its parent.
	out := DoStat(StatJob{Abs: filepath.Join(t.TempDir(), "gone"), Type: diffmodel.File})
	if out.Err == nil {
		t.Fatal("Err = nil; want the lstat failure")
	}
}

func TestDoCompareChecksum(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()

	// Same size, differing content only near the end — verifies the
	// comparison actually reads content rather than just trusting size.
	mustWrite(t, filepath.Join(left, "same"), "abcdefgh")
	mustWrite(t, filepath.Join(right, "same"), "abcdefgh")
	mustWrite(t, filepath.Join(left, "diff"), "abcdefgh")
	mustWrite(t, filepath.Join(right, "diff"), "abcdefgX")

	sameOut := DoCompare(CompareJob{
		LeftAbs: filepath.Join(left, "same"), RightAbs: filepath.Join(right, "same"),
		Type: diffmodel.File, Level: diffmodel.Checksum,
	})
	if sameOut.Result != diffmodel.Same {
		t.Fatalf("Result = %v; want Same", sameOut.Result)
	}

	diffOut := DoCompare(CompareJob{
		LeftAbs: filepath.Join(left, "diff"), RightAbs: filepath.Join(right, "diff"),
		Type: diffmodel.File, Level: diffmodel.Checksum,
	})
	if diffOut.Result != diffmodel.Differs {
		t.Fatalf("Result = %v; want Differs", diffOut.Result)
	}
}

func TestDoCompareChecksumAcrossChunkBoundary(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()

	big := make([]byte, 200*1024) // > one 64KB chunk
	for i := range big {
		big[i] = byte(i % 251)
	}
	bigDiff := append([]byte(nil), big...)
	bigDiff[150*1024] ^= 0xFF // differ in a later chunk only

	if err := os.WriteFile(filepath.Join(left, "big"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(right, "big"), bigDiff, 0o644); err != nil {
		t.Fatal(err)
	}

	out := DoCompare(CompareJob{
		LeftAbs: filepath.Join(left, "big"), RightAbs: filepath.Join(right, "big"),
		Type: diffmodel.File, Level: diffmodel.Checksum,
	})
	if out.Result != diffmodel.Differs {
		t.Fatalf("Result = %v; want Differs", out.Result)
	}
}

func TestDoCompareUnreadableFileIsError(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	mustWrite(t, filepath.Join(right, "f"), "x")
	// left/f deliberately does not exist -> Lstat fails.
	out := DoCompare(CompareJob{
		LeftAbs: filepath.Join(left, "f"), RightAbs: filepath.Join(right, "f"),
		Type: diffmodel.File, Level: diffmodel.SizeMtime,
	})
	if out.Result != diffmodel.CompareError {
		t.Fatalf("Result = %v; want CompareError", out.Result)
	}
	if out.Err == nil {
		t.Fatal("Err = nil; want non-nil")
	}
}
