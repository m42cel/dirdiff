package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/scan"
	"github.com/m42cel/dirdiff/internal/session"
	"github.com/m42cel/dirdiff/internal/sidetree"
)

// newTestModel builds a Model over a real Session on two empty temp
// directories: empty so the background listing of the roots finds nothing,
// leaving the tree entirely under the control of the synthetic results each
// test feeds in — the same way package session's own tests drive it.
func newTestModel(t *testing.T) (Model, *session.Session) {
	t.Helper()
	sess := session.New(t.TempDir(), t.TempDir(), 1, 1, diffmodel.NotCompared)
	t.Cleanup(sess.Close)
	m := New(sess)
	m.width, m.height = 100, 30
	return m, sess
}

// entries spells a listing compactly: a trailing "/" makes a directory,
// "@" a symlink, anything else a file.
func entries(spec ...string) []diffmodel.ListedEntry {
	out := make([]diffmodel.ListedEntry, 0, len(spec))
	for _, s := range spec {
		e := diffmodel.ListedEntry{Name: s, Type: diffmodel.File}
		switch {
		case strings.HasSuffix(s, "/"):
			e.Name, e.Type = strings.TrimSuffix(s, "/"), diffmodel.Dir
		case strings.HasSuffix(s, "@"):
			e.Name, e.Type = strings.TrimSuffix(s, "@"), diffmodel.Symlink
		}
		out = append(out, e)
	}
	return out
}

// listBoth feeds the same listing of relPath to both sides, the way two
// listing jobs over identical directories would deliver it.
func listBoth(sess *session.Session, relPath string, spec ...string) {
	for _, sd := range diffmodel.Sides {
		listSide(sess, sd, relPath, spec...)
	}
}

// listSide feeds one side's listing alone, for entries that exist on
// only one of the two trees.
func listSide(sess *session.Session, sd diffmodel.Side, relPath string, spec ...string) {
	sess.OnListResult(scan.ListResult{Side: sd, RelPath: relPath, Entries: entries(spec...)})
}

// statBoth feeds each side's metadata for one row, which is what a
// metadata verdict and a directory's size totals are made of.
func statBoth(sess *session.Session, relPath string, leftSize, rightSize int64) {
	sizes := [2]int64{diffmodel.Left: leftSize, diffmodel.Right: rightSize}
	for _, sd := range diffmodel.Sides {
		sess.OnStatResult(scan.StatResult{Side: sd, RelPath: relPath, Size: sizes[sd], Mtime: time.Unix(1, 0)})
	}
}

func press(t *testing.T, m Model, key string) Model {
	t.Helper()
	next, _ := m.handleSingleKey(key)
	return next.(Model)
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		// Base 2 throughout: 1000000 bytes is under a MiB, unlike under the
		// base-10 units some tools print.
		{1000000, "977 KiB"},
		{1048576, "1.0 MiB"},
		{10 * 1048576, "10 MiB"},
		{3 * 1024 * 1024 * 1024, "3.0 GiB"},
		{1 << 60, "1.0 EiB"},
	}
	for _, c := range cases {
		if got := humanSize(c.bytes); got != c.want {
			t.Errorf("humanSize(%d) = %q; want %q", c.bytes, got, c.want)
		}
	}
}

func TestFileSizeLabelKeepsExactBytes(t *testing.T) {
	// Two files a byte apart compare as different at the metadata level, so
	// the rounded size alone would show a difference the user can't see.
	if got, want := fileSizeLabel(1048576), "1.0 MiB (1048576 B)"; got != want {
		t.Errorf("fileSizeLabel = %q; want %q", got, want)
	}
	if got, want := fileSizeLabel(12), "12 B"; got != want {
		t.Errorf("fileSizeLabel = %q; want %q — no point repeating a small byte count twice", got, want)
	}
}

func TestTotalsLabel(t *testing.T) {
	cases := []struct {
		name   string
		totals sidetree.Totals
		want   string
	}{{
		name:   "nothing statted yet reports an unknown size",
		totals: sidetree.Totals{Dirs: 2, Files: 3},
		want:   "2 directories · 3 files · size ?",
	}, {
		name:   "partially statted size is a lower bound",
		totals: sidetree.Totals{Dirs: 2, Files: 4, Size: 1024, SizedFiles: 1},
		want:   "2 directories · 4 files · ≥1.0 KiB (1/4 files sized)",
	}, {
		name:   "fully statted size is exact",
		totals: sidetree.Totals{Dirs: 1, Files: 2, Size: 2048, SizedFiles: 2},
		want:   "1 directory · 2 files · 2.0 KiB",
	}, {
		name:   "empty directory has nothing unknown about it",
		totals: sidetree.Totals{},
		want:   "0 directories · 0 files · 0 B",
	}, {
		name:   "symlinks are counted apart from files and never sized",
		totals: sidetree.Totals{Files: 1, Symlinks: 2, Size: 512, SizedFiles: 1},
		want:   "0 directories · 1 file · 2 links · 512 B",
	}, {
		name:   "every count reads singular at exactly one",
		totals: sidetree.Totals{Dirs: 1, Files: 1, Symlinks: 1, Size: 512, SizedFiles: 1},
		want:   "1 directory · 1 file · 1 link · 512 B",
	}}
	for _, c := range cases {
		if got := totalsLabel(c.totals); got != c.want {
			t.Errorf("%s: totalsLabel = %q; want %q", c.name, got, c.want)
		}
	}
}

func TestDetailsPanelShowsDirectoryTotals(t *testing.T) {
	m, sess := newTestModel(t)
	listBoth(sess, "", "sub/")
	listBoth(sess, "sub", "a.txt", "b.txt")

	// Cursor on "sub" in the root listing: its totals are its subtree's,
	// not its own.
	details := m.renderDetails()
	if !strings.Contains(details, "0 directories · 2 files · size ?") {
		t.Errorf("unstatted directory details = %q; want the file count with no size yet", details)
	}

	statBoth(sess, "sub/a.txt", 1024, 1024)
	details = m.renderDetails()
	if !strings.Contains(details, "≥1.0 KiB (1/2 files sized)") {
		t.Errorf("partly statted directory details = %q; want a lower-bound size", details)
	}

	statBoth(sess, "sub/b.txt", 1024, 3072)
	details = m.renderDetails()
	if !strings.Contains(details, "left:  0 directories · 2 files · 2.0 KiB") {
		t.Errorf("fully statted directory details = %q; want an exact left total", details)
	}
	if !strings.Contains(details, "right: 0 directories · 2 files · 4.0 KiB") {
		t.Errorf("fully statted directory details = %q; want the right side totalled separately", details)
	}
}

func TestDetailsPanelMarksMissingSideAsNotExisting(t *testing.T) {
	m, sess := newTestModel(t)
	listSide(sess, diffmodel.Left, "", "left-only/")
	listSide(sess, diffmodel.Right, "", "right-only/")
	listSide(sess, diffmodel.Left, "left-only", "a.txt")
	listSide(sess, diffmodel.Right, "right-only")

	lines := func() []string { return strings.Split(m.renderDetails(), "\n") }

	// left-only, sorted first (dirs-then-alpha): its right side has nothing
	// at all under it — no subtree exists there to total — so that line
	// names it as absent, not "0 directories · 0 files · 0 B".
	got := lines()
	if got[1] != "left:  0 directories · 1 file · size ?" {
		t.Errorf("left-only dir's left line = %q; want its real (unstatted) totals", got[1])
	}
	if got[2] != "right: "+doesNotExistText {
		t.Errorf("left-only dir's right line = %q; want the does-not-exist placeholder, not a zeroed total", got[2])
	}

	m.cursorIdx = 1
	got = lines()
	if got[1] != "left:  "+doesNotExistText {
		t.Errorf("right-only dir's left line = %q; want the does-not-exist placeholder, not a zeroed total", got[1])
	}
	if got[2] != "right: 0 directories · 0 files · 0 B" {
		t.Errorf("right-only dir's right line = %q; want its real (empty) totals", got[2])
	}
}

func TestAscendAboveRootShowsBothRootsAsOneRow(t *testing.T) {
	m, sess := newTestModel(t)
	listBoth(sess, "", "a.txt")
	statBoth(sess, "a.txt", 2048, 2048)

	// Wide enough that the title line naming both temp-dir paths isn't
	// truncated before the assertion below can find them.
	m.width = 400

	m = press(t, m, "left")
	if !m.atRootParent {
		t.Fatal("left at the root should go up to the root-pair level")
	}
	visible := m.visibleChildren()
	if len(visible) != 1 || visible[0] != sess.Tree() {
		t.Fatalf("root-parent level should show exactly the root pair, got %d rows", len(visible))
	}

	details := m.renderDetails()
	if !strings.Contains(details, sess.LeftRoot) || !strings.Contains(details, sess.RightRoot) {
		t.Errorf("details = %q; want both root paths named, since the root row has no name of its own", details)
	}
	if !strings.Contains(details, "0 directories · 1 file · 2.0 KiB") {
		t.Errorf("details = %q; want the whole tree's totals", details)
	}

	// There's nothing above that level, and the filter can't hide the only
	// row leading back down.
	m = press(t, m, "left")
	if !m.atRootParent || len(m.visibleChildren()) != 1 {
		t.Error("left again at the root-parent level should stay put, not empty the pane")
	}
	m.filter = setOf(FilterDifferent)
	if len(m.visibleChildren()) != 1 {
		t.Error("the root row must stay visible under any filter")
	}
	m.filter = defaultFilterSet()

	m = press(t, m, "right")
	if m.atRootParent {
		t.Fatal("entering the root row should descend back into the root listing")
	}
	if got := m.visibleChildren(); len(got) != 1 || got[0].Name() != "a.txt" {
		t.Fatalf("back in the root listing, want the a.txt row; got %v", got)
	}
}

func TestRootParentPaneTitlesAndRowNames(t *testing.T) {
	m, sess := newTestModel(t)
	m.atRootParent = true

	if got, want := m.paneTitle(diffmodel.Left), filepath.Dir(sess.LeftRoot); got != want {
		t.Errorf("pane title = %q; want the root's parent %q", got, want)
	}
	if got, want := m.rootRowLabel(diffmodel.Left), filepath.Base(sess.LeftRoot); got != want {
		t.Errorf("root row label = %q; want the root's own last element %q", got, want)
	}
	if got, want := rootRowName("/tmp/alpha/left"), "left"; got != want {
		t.Errorf("root row name = %q; want %q", got, want)
	}
	// A root with no last element of its own keeps its whole path, so the
	// row never degenerates into a bare "." or "/".
	for _, root := range []string{"/", ".", ".."} {
		if got := rootRowName(root); got != root {
			t.Errorf("rootRowName(%q) = %q; want the path itself", root, got)
		}
	}
}
