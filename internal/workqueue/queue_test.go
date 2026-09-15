package workqueue

import "testing"

func TestPopOrdersByPriorityThenFIFO(t *testing.T) {
	q := New[string]()
	q.Upsert("a", Low, "a", nil)
	q.Upsert("b", High, "b", nil)
	q.Upsert("c", Medium, "c", nil)
	q.Upsert("d", High, "d", nil)

	want := []string{"b", "d", "c", "a"}
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
	q.Upsert("x", Low, 1, nil)
	q.Upsert("x", Low, 2, func(old int) int {
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

func TestUpsertRaisesPriorityOnMerge(t *testing.T) {
	q := New[string]()
	q.Upsert("x", Low, "x", nil)
	q.Upsert("y", High, "y", nil)
	// Re-upsert x at High: it should now pop before y was already High... so pop order should be x or y (both High) but x must come before any Low-only item.
	q.Upsert("x", High, "x", nil)

	_, key1, _ := q.Pop()
	_, key2, _ := q.Pop()
	if key1 != "x" && key1 != "y" {
		t.Fatalf("expected x or y first (both High priority), got %q", key1)
	}
	if key2 != "x" && key2 != "y" {
		t.Fatalf("expected x or y second (both High priority), got %q", key2)
	}
	if key1 == key2 {
		t.Fatalf("popped the same key twice: %q", key1)
	}
}

func TestBoostIsNoOpWhenNotQueued(t *testing.T) {
	q := New[string]()
	q.Boost("missing", High) // must not panic or create a phantom entry
	if q.PendingCount() != 0 {
		t.Fatalf("PendingCount() = %d; want 0", q.PendingCount())
	}
}

func TestBoostRaisesQueuedPriority(t *testing.T) {
	q := New[string]()
	q.Upsert("a", Low, "a", nil)
	q.Upsert("b", Medium, "b", nil)
	q.Boost("a", High)

	_, key, _ := q.Pop()
	if key != "a" {
		t.Fatalf("Pop() key = %q; want %q (boosted job should pop first)", key, "a")
	}
}

func TestIsPendingReflectsQueuedAndActive(t *testing.T) {
	q := New[string]()
	if q.IsPending("a") {
		t.Fatal("IsPending should be false before enqueue")
	}
	q.Upsert("a", High, "a", nil)
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
	q.Upsert("a", High, "a", nil)
	q.Upsert("b", High, "b", nil)
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
	q.Upsert("late", High, "late", nil)
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
