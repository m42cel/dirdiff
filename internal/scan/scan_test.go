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

func TestDoCompareSizeMtimeDetectsSizeDifference(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	mustWrite(t, filepath.Join(left, "f"), "hello")
	mustWrite(t, filepath.Join(right, "f"), "hello!!!")

	out := DoCompare(CompareJob{
		LeftAbs: filepath.Join(left, "f"), RightAbs: filepath.Join(right, "f"),
		Type: diffmodel.File, Level: diffmodel.SizeMtime,
	})
	if out.Result != diffmodel.Differs {
		t.Fatalf("Result = %v; want Differs (sizes differ)", out.Result)
	}
	if out.Stat == nil || out.Stat.LeftSize != 5 || out.Stat.RightSize != 8 {
		t.Fatalf("Stat = %+v; want LeftSize=5 RightSize=8", out.Stat)
	}
}

func TestDoCompareSizeMtimeDetectsMtimeOnlyDifference(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	mustWrite(t, filepath.Join(left, "f"), "hello")
	mustWrite(t, filepath.Join(right, "f"), "hello")

	now := time.Now()
	if err := os.Chtimes(filepath.Join(left, "f"), now, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	if err := os.Chtimes(filepath.Join(right, "f"), later, later); err != nil {
		t.Fatal(err)
	}

	// Sizes are equal but mtimes differ — the merged size+mtime level must
	// catch this even though a size-only check wouldn't.
	out := DoCompare(CompareJob{
		LeftAbs: filepath.Join(left, "f"), RightAbs: filepath.Join(right, "f"),
		Type: diffmodel.File, Level: diffmodel.SizeMtime,
	})
	if out.Result != diffmodel.Differs {
		t.Fatalf("Result = %v; want Differs (mtimes differ)", out.Result)
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

func TestDoCompareSymlinkComparesTargetString(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()

	if err := os.Symlink("/target/a", filepath.Join(left, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/target/a", filepath.Join(right, "link")); err != nil {
		t.Fatal(err)
	}
	out := DoCompare(CompareJob{
		LeftAbs: filepath.Join(left, "link"), RightAbs: filepath.Join(right, "link"),
		Type: diffmodel.Symlink, Level: diffmodel.SizeMtime, // level should be irrelevant for symlinks
	})
	if out.Result != diffmodel.Same {
		t.Fatalf("Result = %v; want Same (identical targets)", out.Result)
	}

	if err := os.Symlink("/target/b", filepath.Join(right, "link2")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/target/other", filepath.Join(left, "link2")); err != nil {
		t.Fatal(err)
	}
	out2 := DoCompare(CompareJob{
		LeftAbs: filepath.Join(left, "link2"), RightAbs: filepath.Join(right, "link2"),
		Type: diffmodel.Symlink, Level: diffmodel.Checksum,
	})
	if out2.Result != diffmodel.Differs {
		t.Fatalf("Result = %v; want Differs (different targets)", out2.Result)
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
