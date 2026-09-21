package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/scan"
)

// collect runs a wait command and returns the batch it delivered.
func collect(t *testing.T, cmd tea.Cmd) []scan.ListResult {
	t.Helper()
	msg := cmd()
	if msg == nil {
		return nil
	}
	batch, ok := msg.(listResultsMsg)
	if !ok {
		t.Fatalf("got %T; want listResultsMsg", msg)
	}
	return batch.rs
}

// The point of the drain: results that are already queued when one is
// taken ride along in the same message, so the burst costs one render
// instead of one per result.
func TestWaitResultsDrainsWhatIsAlreadyQueued(t *testing.T) {
	ch := make(chan scan.ListResult, 8)
	for _, p := range []string{"a", "b", "c"} {
		ch <- scan.ListResult{RelPath: p}
	}

	got := collect(t, waitListResults(ch))
	if len(got) != 3 {
		t.Fatalf("batched %d results; want all 3 queued ones", len(got))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].RelPath != want {
			t.Errorf("result %d is %q; want %q — arrival order must be preserved", i, got[i].RelPath, want)
		}
	}
}

// A lone result must not wait around for company: the drain only takes
// what is there already, so a trickle has no added latency.
func TestWaitResultsDeliversASingleResultImmediately(t *testing.T) {
	ch := make(chan scan.ListResult, 8)
	ch <- scan.ListResult{RelPath: "only"}

	done := make(chan tea.Msg, 1)
	go func() { done <- waitListResults(ch)() }()

	select {
	case msg := <-done:
		got, ok := msg.(listResultsMsg)
		if !ok {
			t.Fatalf("got %T; want listResultsMsg", msg)
		}
		if len(got.rs) != 1 || got.rs[0].RelPath != "only" {
			t.Fatalf("got %v; want just the one queued result", got.rs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the drain blocked waiting for a second result")
	}
}

// The drain is bounded, so a worker pool feeding the channel faster than
// the UI drains it can't keep a keypress waiting indefinitely.
func TestWaitResultsStopsAtTheBatchCap(t *testing.T) {
	ch := make(chan scan.ListResult, resultBatchMax*2)
	for i := 0; i < resultBatchMax+10; i++ {
		ch <- scan.ListResult{RelPath: "x"}
	}

	if got := collect(t, waitListResults(ch)); len(got) != resultBatchMax {
		t.Fatalf("batched %d results; want the cap of %d", len(got), resultBatchMax)
	}
	if len(ch) != 10 { // the rest stays for the next wait
		t.Fatalf("%d results left queued; want 10", len(ch))
	}
}

// A channel that closes mid-drain still hands over what was read; the
// next wait is the one that reports the end by returning no message,
// which is what stops the UI's listening loop.
func TestWaitResultsHandlesACloseMidDrain(t *testing.T) {
	ch := make(chan scan.ListResult, 8)
	ch <- scan.ListResult{RelPath: "a"}
	ch <- scan.ListResult{RelPath: "b"}
	close(ch)

	if got := collect(t, waitListResults(ch)); len(got) != 2 {
		t.Fatalf("batched %d results; want both of the queued ones", len(got))
	}
	if msg := waitListResults(ch)(); msg != nil {
		t.Fatalf("got %#v from a drained closed channel; want nil to end the loop", msg)
	}
}

// Every result in a batch has to reach the session — batching is a
// rendering optimization, not a place where results get dropped.
func TestUpdateAppliesEveryResultInABatch(t *testing.T) {
	m, _ := newTestModel(t)

	next, _ := m.Update(listResultsMsg{[]scan.ListResult{
		{Side: diffmodel.Left, RelPath: "", Entries: entries("both.txt", "left.txt")},
		{Side: diffmodel.Right, RelPath: "", Entries: entries("both.txt")},
	}})
	m = next.(Model)

	var names []string
	for _, c := range m.visibleChildren() {
		names = append(names, c.Name())
	}
	if len(names) != 2 {
		t.Fatalf("rows %v; want both listings applied", names)
	}
}
