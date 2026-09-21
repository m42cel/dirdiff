package pairtree

import (
	"testing"
	"time"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/sidetree"
)

var (
	t1 = time.Unix(1_700_000_000, 0)
	t2 = time.Unix(1_700_003_600, 0)
)

func TestMetadataVerdictWaitsForBothSides(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothF("f.txt"))
	n := f.find("f.txt")

	f.stat("f.txt", diffmodel.Left, sidetree.Stat{Size: 100, Mtime: t1})
	if n.Result != diffmodel.Unknown || n.Level != diffmodel.NotCompared {
		t.Fatalf("Result=%v Level=%v with one side measured; want nothing decided yet", n.Result, n.Level)
	}

	f.stat("f.txt", diffmodel.Right, sidetree.Stat{Size: 100, Mtime: t1})
	if n.Result != diffmodel.Same || n.Level != diffmodel.SizeMtime {
		t.Fatalf("Result=%v Level=%v; want Same at the metadata level, decided in memory", n.Result, n.Level)
	}
}

func TestMetadataVerdictNeedsSizeAndMtime(t *testing.T) {
	cases := []struct {
		name        string
		left, right sidetree.Stat
		want        diffmodel.CompareResult
	}{
		{"same size and mtime", sidetree.Stat{Size: 10, Mtime: t1}, sidetree.Stat{Size: 10, Mtime: t1}, diffmodel.Same},
		{"size differs", sidetree.Stat{Size: 10, Mtime: t1}, sidetree.Stat{Size: 11, Mtime: t1}, diffmodel.Differs},
		// A size-only check wouldn't catch this one.
		{"mtime differs", sidetree.Stat{Size: 10, Mtime: t1}, sidetree.Stat{Size: 10, Mtime: t2}, diffmodel.Differs},
	}
	for _, c := range cases {
		f := newFixture()
		f.list(f.root, bothF("f.txt"))
		f.statBoth("f.txt", c.left, c.right)
		if got := f.find("f.txt").Result; got != c.want {
			t.Errorf("%s: Result = %v; want %v", c.name, got, c.want)
		}
	}
}

// A symlink's target string is the whole of what can be compared
// (SPEC.md §7), so its verdict is final rather than provisional — it's
// recorded at the deepest level, which is also what keeps a later
// content trigger from queueing a job to open it.
func TestSymlinkVerdictComesFromTheTargetAtTheDeepestLevel(t *testing.T) {
	for _, c := range []struct {
		name        string
		left, right string
		want        diffmodel.CompareResult
	}{
		{"same target", "/a/b", "/a/b", diffmodel.Same},
		{"different target", "/a/b", "/a/c", diffmodel.Differs},
	} {
		f := newFixture()
		f.list(f.root, bothL("link"))
		f.statBoth("link",
			sidetree.Stat{Size: 4, Mtime: t1, LinkTarget: c.left},
			sidetree.Stat{Size: 4, Mtime: t2, LinkTarget: c.right})

		n := f.find("link")
		if n.Result != c.want {
			t.Errorf("%s: Result = %v; want %v — the differing mtimes must not decide it", c.name, n.Result, c.want)
		}
		if n.Level != diffmodel.Checksum {
			t.Errorf("%s: Level = %v; want Checksum, since there is nothing deeper left to read", c.name, n.Level)
		}
	}
}

func TestFailedStatMakesTheRowAnError(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothF("f.txt"))
	f.statBoth("f.txt",
		sidetree.Stat{Err: errPermission},
		sidetree.Stat{Size: 10, Mtime: t1})

	n := f.find("f.txt")
	if n.Result != diffmodel.CompareError {
		t.Fatalf("Result = %v; want CompareError — an unreadable side is not a difference", n.Result)
	}
	if n.Err != errPermission {
		t.Errorf("Err = %v; want the stat failure carried through", n.Err)
	}
}

// Monotonicity (SPEC.md §5.3): a content verdict outranks the metadata
// one, whichever order they arrive in.
func TestContentVerdictSurvivesALaterMetadataVerdict(t *testing.T) {
	f := newFixture()
	f.list(f.root, bothF("f.txt"))
	n := f.find("f.txt")

	ApplyCompareResult(n, diffmodel.Checksum, diffmodel.Same, nil)
	f.statBoth("f.txt", sidetree.Stat{Size: 10, Mtime: t1}, sidetree.Stat{Size: 10, Mtime: t2})

	if n.Level != diffmodel.Checksum || n.Result != diffmodel.Same {
		t.Fatalf("Level=%v Result=%v; want the content verdict kept, not replaced by differing mtimes", n.Level, n.Result)
	}
}

func TestMetadataVerdictLandsOnRowsCreatedAfterTheStat(t *testing.T) {
	// The left side is listed and measured before the right side has even
	// listed, so the row is created knowing both sides already — which is
	// the situation every pairing opened over a scanned subtree is in.
	f := newFixture()
	f.listSide(f.root, diffmodel.Left, bothF("f.txt"))
	sidetree.ApplyStat(f.trees[diffmodel.Left].Index["f.txt"], sidetree.Stat{Size: 10, Mtime: t1})

	rt := f.trees[diffmodel.Right]
	rt.ApplyListing(rt.Root, []diffmodel.ListedEntry{{Name: "f.txt", Type: diffmodel.File}}, nil)
	sidetree.ApplyStat(rt.Index["f.txt"], sidetree.Stat{Size: 10, Mtime: t1})
	f.merge(f.root)

	n := f.find("f.txt")
	if n.Result != diffmodel.Same || n.Level != diffmodel.SizeMtime {
		t.Fatalf("Result=%v Level=%v; want the verdict to fall out as the row is created, with no job", n.Result, n.Level)
	}
	if got := f.root.DescendantsWithStatus(diffmodel.RowEqual); got != 1 {
		t.Errorf("equal tally = %d; want 1 — a verdict applied at creation still has to reach the tallies", got)
	}
}

func TestOneSidedRowNeverGetsAVerdict(t *testing.T) {
	f := newFixture()
	f.list(f.root, leftF("gone.txt"))
	f.stat("gone.txt", diffmodel.Left, sidetree.Stat{Size: 10, Mtime: t1})

	n := f.find("gone.txt")
	if n.Level != diffmodel.NotCompared || n.Result != diffmodel.Unknown {
		t.Fatalf("Level=%v Result=%v; want neither — there is nothing to compare against", n.Level, n.Result)
	}
	// The size is still worth having: it's what lets a one-sided subtree
	// report a total instead of "size ?" (SPEC.md §4.2).
	if left, _ := f.root.SideTotals(); left.Size != 10 || left.SizedFiles != 1 {
		t.Errorf("left totals = %d bytes over %d files; want 10 over 1", left.Size, left.SizedFiles)
	}
}
