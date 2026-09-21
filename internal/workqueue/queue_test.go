package workqueue

import (
	"testing"
	"time"
)

func TestPopOrdersByArrivalWhenNoFocusDistinguishes(t *testing.T) {
	q := New[string]()
	q.Upsert("a", "a", nil)
	q.Upsert("b", "b", nil)
	q.Upsert("c", "c", nil)

	want := []string{"a", "b", "c"}
	for _, w := range want {
		payload, key, ok := q.Pop()
		if !ok || payload != w || key != w {
			t.Fatalf("Pop() = %q, %q, %v; want %q", payload, key, ok, w)
		}
		q.Done(key)
	}
}

func TestUpsertMergesExistingKey(t *testing.T) {
	q := New[int]()
	q.Upsert("x", 1, nil)
	q.Upsert("x", 2, func(old int) int {
		if 2 > old {
			return 2
		}
		return old
	})
	if q.PendingCount() != 1 {
		t.Fatalf("PendingCount() = %d; want 1 (should have merged, not duplicated)", q.PendingCount())
	}
	payload, _, _ := q.Pop()
	if payload != 2 {
		t.Fatalf("payload = %d; want 2 (merge should have kept the larger value)", payload)
	}
}

func TestFocusPrioritizesDescendantsOverEverythingElse(t *testing.T) {
	q := New[string]()
	q.Upsert("elsewhere", "elsewhere", nil)
	q.Upsert("dir/deep/nested", "dir/deep/nested", nil)
	q.Upsert("dir", "dir", nil)
	q.SetFocus("dir")

	want := []string{"dir", "dir/deep/nested", "elsewhere"}
	for _, w := range want {
		_, key, _ := q.Pop()
		if key != w {
			t.Fatalf("Pop() key = %q; want %q", key, w)
		}
	}
}

func TestFocusOrdersDescendantsByDepth(t *testing.T) {
	q := New[string]()
	q.Upsert("dir/a/b/c", "dir/a/b/c", nil)
	q.Upsert("dir/a", "dir/a", nil)
	q.Upsert("dir/a/b", "dir/a/b", nil)
	q.SetFocus("dir")

	want := []string{"dir/a", "dir/a/b", "dir/a/b/c"}
	for _, w := range want {
		_, key, _ := q.Pop()
		if key != w {
			t.Fatalf("Pop() key = %q; want %q (shallower descendants should pop first)", key, w)
		}
	}
}

func TestFocusOrdersNonDescendantsBySiblingDistance(t *testing.T) {
	q := New[string]()
	// "cousin" is under a different top-level directory than "dir" (two
	// hops further away than "sibling", which shares dir's parent).
	q.Upsert("cousin/child", "cousin/child", nil)
	q.Upsert("sibling", "sibling", nil)
	q.SetFocus("dir")

	_, key, _ := q.Pop()
	if key != "sibling" {
		t.Fatalf("Pop() key = %q; want %q (closer sibling should pop before a farther cousin)", key, "sibling")
	}
}

func TestSetFocusReordersAlreadyQueuedJobs(t *testing.T) {
	q := New[string]()
	q.Upsert("a", "a", nil)
	q.Upsert("b/child", "b/child", nil)
	// With no focus, "a" (queued first) pops first.
	q.SetFocus("b")

	_, key, _ := q.Pop()
	if key != "b/child" {
		t.Fatalf("Pop() key = %q; want %q (SetFocus must reorder jobs queued before the focus change)", key, "b/child")
	}
}

func TestSetFociRanksEachNamespaceAgainstItsOwnFocus(t *testing.T) {
	q := New[string]()
	for _, key := range []string{"L/other", "L/dir/child", "R/other", "R/dir/child"} {
		q.Upsert(key, key, nil)
	}
	q.SetFoci("L/dir", "R/dir")

	// Each side's focused subtree outranks that side's unrelated work, and
	// the two sides interleave by their own distances — never by a
	// distance measured against the other side's focus.
	for _, want := range []string{"L/dir/child", "R/dir/child", "L/other", "R/other"} {
		_, key, _ := q.Pop()
		if key != want {
			t.Fatalf("Pop() key = %q; want %q", key, want)
		}
	}
}

// A key must never be measured against another namespace's focus: the
// two trees share no ancestor, so the "distance" between them is just
// the sum of two depths, which can undercut a genuine same-tree
// distance. Here R/other is a real 4 hops from the right pane but would
// look like 3 hops from the left pane's root.
func TestSetFociNeverMeasuresAcrossNamespaces(t *testing.T) {
	q := New[string]()
	q.Upsert("R/other", "R/other", nil)
	q.Upsert("R/very/deep/dir/child", "R/very/deep/dir/child", nil)
	q.SetFoci("L", "R/very/deep/dir")

	_, key, _ := q.Pop()
	if key != "R/very/deep/dir/child" {
		t.Fatalf("Pop() key = %q; want %q — the right pane's own subtree, not the job that happens to look close to the left root", key, "R/very/deep/dir/child")
	}
}

// A namespace nobody is focused on — a suspended pairing's leftovers, or
// anything before the first navigation — still pops breadth-first within
// its own tree rather than jumping the queue.
func TestSetFociLeavesAnUnfocusedNamespaceInDepthOrder(t *testing.T) {
	q := New[string]()
	q.Upsert("p1/a/b", "p1/a/b", nil)
	q.Upsert("p1/a", "p1/a", nil)
	q.Upsert("L/dir/child", "L/dir/child", nil)
	q.SetFoci("L/dir")

	want := []string{"L/dir/child", "p1/a", "p1/a/b"}
	for _, w := range want {
		_, key, _ := q.Pop()
		if key != w {
			t.Fatalf("Pop() key = %q; want %q", key, w)
		}
	}
}

func TestSetFocusDoesNotAffectAlreadyActiveJobs(t *testing.T) {
	q := New[string]()
	q.Upsert("a", "a", nil)
	q.Upsert("b", "b", nil)
	_, activeKey, _ := q.Pop() // pops "a" (queued first), now in-flight

	q.SetFocus("b")
	if !q.IsPending(activeKey) {
		t.Fatal("SetFocus must not affect an already-active (in-flight) job")
	}
}

func TestIsPendingReflectsQueuedAndActive(t *testing.T) {
	q := New[string]()
	if q.IsPending("a") {
		t.Fatal("IsPending should be false before enqueue")
	}
	q.Upsert("a", "a", nil)
	if !q.IsPending("a") {
		t.Fatal("IsPending should be true while queued")
	}
	_, key, _ := q.Pop()
	if !q.IsPending(key) {
		t.Fatal("IsPending should be true while in-flight (popped, not yet Done)")
	}
	q.Done(key)
	if q.IsPending(key) {
		t.Fatal("IsPending should be false after Done")
	}
}

func TestClearDropsOnlyQueuedNotActive(t *testing.T) {
	q := New[string]()
	q.Upsert("a", "a", nil)
	q.Upsert("b", "b", nil)
	_, activeKey, _ := q.Pop() // pops one of a/b, now in-flight

	q.Clear()
	if q.PendingCount() != 0 {
		t.Fatalf("PendingCount() = %d after Clear(); want 0", q.PendingCount())
	}
	if !q.IsPending(activeKey) {
		t.Fatal("Clear() must not affect an already-active (in-flight) job")
	}
}

func TestPopBlocksUntilUpsert(t *testing.T) {
	q := New[string]()
	done := make(chan struct{})
	go func() {
		payload, _, ok := q.Pop()
		if !ok || payload != "late" {
			t.Errorf("Pop() = %q, %v; want %q, true", payload, ok, "late")
		}
		close(done)
	}()
	q.Upsert("late", "late", nil)
	<-done
}

func TestCloseUnblocksPop(t *testing.T) {
	q := New[string]()
	done := make(chan struct{})
	go func() {
		_, _, ok := q.Pop()
		if ok {
			t.Error("Pop() after Close() should return ok=false")
		}
		close(done)
	}()
	q.Close()
	<-done
}

func TestPopUnlessStillDeliversAJobWhenStopIsFalse(t *testing.T) {
	q := New[string]()
	q.Upsert("a", "a", nil)
	payload, key, ok := q.PopUnless(func() bool { return false })
	if !ok || payload != "a" || key != "a" {
		t.Fatalf("PopUnless() = %q, %q, %v; want %q, %q, true", payload, key, ok, "a", "a")
	}
}

func TestWakeLetsPopUnlessStopOnAnEmptyQueue(t *testing.T) {
	q := New[string]()
	done := make(chan struct{})
	var stopSeen bool
	go func() {
		_, _, ok := q.PopUnless(func() bool { stopSeen = true; return true })
		if ok {
			t.Error("PopUnless() should return ok=false once stop reports true")
		}
		close(done)
	}()
	// Give the goroutine a chance to actually block in Wait() before
	// waking it, so this test exercises the "already idle" path rather
	// than stop() winning the race on first entry.
	time.Sleep(20 * time.Millisecond)
	q.Wake()
	<-done
	if !stopSeen {
		t.Fatal("stop() was never invoked")
	}
}

func TestPopUnlessStopsImmediatelyEvenWithQueuedBacklog(t *testing.T) {
	q := New[string]()
	q.Upsert("a", "a", nil)
	q.Upsert("b", "b", nil)
	q.Upsert("c", "c", nil)

	_, _, ok := q.PopUnless(func() bool { return true })
	if ok {
		t.Fatal("PopUnless() should return ok=false when stop() is true, even with a non-empty backlog")
	}
	if q.PendingCount() != 3 {
		t.Fatalf("PendingCount() = %d; want 3 (a stopping worker must not consume a queued job)", q.PendingCount())
	}
}

func TestPopUnlessStillDrainsBacklogOnCloseWhenStopStaysFalse(t *testing.T) {
	q := New[string]()
	q.Upsert("a", "a", nil)
	q.Close()

	payload, key, ok := q.PopUnless(func() bool { return false })
	if !ok || payload != "a" || key != "a" {
		t.Fatalf("PopUnless() = %q, %q, %v; want %q, %q, true (Close() must not drop already-queued jobs)", payload, key, ok, "a", "a")
	}
}

func TestWakeDoesNotStopPopUnlessWhenStopStaysFalse(t *testing.T) {
	q := New[string]()
	done := make(chan struct{})
	go func() {
		payload, _, ok := q.PopUnless(func() bool { return false })
		if !ok || payload != "late" {
			t.Errorf("PopUnless() = %q, %v; want %q, true", payload, ok, "late")
		}
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	q.Wake() // must not cause the waiter to give up, since stop() is false
	q.Upsert("late", "late", nil)
	<-done
}
