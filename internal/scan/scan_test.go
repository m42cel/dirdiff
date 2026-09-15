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

func TestDoListMergesByNameAndType(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()

	mustWrite(t, filepath.Join(left, "both.txt"), "x")
	mustWrite(t, filepath.Join(right, "both.txt"), "x")

	mustWrite(t, filepath.Join(left, "leftonly.txt"), "x")
	mustWrite(t, filepath.Join(right, "rightonly.txt"), "x")

	// "conflict" is a directory on the left and a file on the right — they
	// must be matched independently (SPEC.md §3.1), not merged into one
	// "type conflict" row.
	mustMkdir(t, filepath.Join(left, "conflict"))
	mustWrite(t, filepath.Join(right, "conflict"), "x")

	res := DoList(ListJob{RelPath: "", LeftAbs: left, RightAbs: right})

	if res.LeftErr != nil || res.RightErr != nil {
		t.Fatalf("unexpected errors: left=%v right=%v", res.LeftErr, res.RightErr)
	}

	got := map[string]diffmodel.Presence{}
	gotType := map[string]diffmodel.EntryType{}
	for _, c := range res.Children {
		got[c.Name] = c.Presence
		gotType[c.Name] = c.Type
	}

	want := map[string]diffmodel.Presence{
		"both.txt":      diffmodel.Both,
		"leftonly.txt":  diffmodel.LeftOnly,
		"rightonly.txt": diffmodel.RightOnly,
	}
	for name, p := range want {
		if got[name] != p {
			t.Errorf("%s: Presence = %v; want %v", name, got[name], p)
		}
	}

	// "conflict" should appear twice: once as a LeftOnly dir, once as a RightOnly file.
	count := 0
	for _, c := range res.Children {
		if c.Name != "conflict" {
			continue
		}
		count++
		switch c.Type {
		case diffmodel.Dir:
			if c.Presence != diffmodel.LeftOnly {
				t.Errorf("conflict dir: Presence = %v; want LeftOnly", c.Presence)
			}
		case diffmodel.File:
			if c.Presence != diffmodel.RightOnly {
				t.Errorf("conflict file: Presence = %v; want RightOnly", c.Presence)
			}
		default:
			t.Errorf("conflict: unexpected type %v", c.Type)
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 'conflict' rows (dir + file), got %d", count)
	}
}

func TestDoListSortsDirsFirstThenAlpha(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()

	for _, name := range []string{"zzz.txt", "aaa.txt"} {
		mustWrite(t, filepath.Join(left, name), "x")
		mustWrite(t, filepath.Join(right, name), "x")
	}
	for _, name := range []string{"zdir", "adir"} {
		mustMkdir(t, filepath.Join(left, name))
		mustMkdir(t, filepath.Join(right, name))
	}

	res := DoList(ListJob{LeftAbs: left, RightAbs: right})
	var names []string
	for _, c := range res.Children {
		names = append(names, c.Name)
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

func TestDoListMissingSideIsNotAnError(t *testing.T) {
	left := t.TempDir()
	right := filepath.Join(left, "does-not-exist")

	res := DoList(ListJob{LeftAbs: left, RightAbs: right})
	if res.RightErr != nil {
		t.Fatalf("RightErr = %v; want nil (missing dir is not an error)", res.RightErr)
	}
}

func TestDoCompareSize(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	mustWrite(t, filepath.Join(left, "f"), "hello")
	mustWrite(t, filepath.Join(right, "f"), "hello!!!")

	out := DoCompare(CompareJob{
		LeftAbs: filepath.Join(left, "f"), RightAbs: filepath.Join(right, "f"),
		Type: diffmodel.File, Level: diffmodel.Size,
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

	sizeOut := DoCompare(CompareJob{
		LeftAbs: filepath.Join(left, "f"), RightAbs: filepath.Join(right, "f"),
		Type: diffmodel.File, Level: diffmodel.Size,
	})
	if sizeOut.Result != diffmodel.Same {
		t.Fatalf("Size level: Result = %v; want Same (equal sizes)", sizeOut.Result)
	}

	mtimeOut := DoCompare(CompareJob{
		LeftAbs: filepath.Join(left, "f"), RightAbs: filepath.Join(right, "f"),
		Type: diffmodel.File, Level: diffmodel.SizeMtime,
	})
	if mtimeOut.Result != diffmodel.Differs {
		t.Fatalf("SizeMtime level: Result = %v; want Differs (mtimes differ)", mtimeOut.Result)
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
		Type: diffmodel.Symlink, Level: diffmodel.Size, // level should be irrelevant for symlinks
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
		Type: diffmodel.File, Level: diffmodel.Size,
	})
	if out.Result != diffmodel.CompareError {
		t.Fatalf("Result = %v; want CompareError", out.Result)
	}
	if out.Err == nil {
		t.Fatal("Err = nil; want non-nil")
	}
}
