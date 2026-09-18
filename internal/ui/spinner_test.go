package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/session"
)

// newTestModel wires a model to a real session over two copies of the
// same directory layout, then applies the root listing the way the
// Update loop does. Everything here drives Model.Update directly — no
// terminal involved.
func newTestModel(t *testing.T, build func(root string)) Model {
	t.Helper()
	left, right := filepath.Join(t.TempDir(), "l"), filepath.Join(t.TempDir(), "r")
	for _, root := range []string{left, right} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		build(root)
	}

	sess := session.New(left, right, 1, 1, diffmodel.SizeMtime)
	t.Cleanup(sess.Close)

	m := New(sess)
	m.width, m.height = 100, 30

	// The worker calls Done before handing the result over, so once this
	// arrives the root listing is no longer counted as outstanding.
	tm, _ := m.Update(listResultMsg{<-sess.ListResults()})
	return tm.(Model)
}

func TestSpinnerStopsOnceNothingIsPending(t *testing.T) {
	m := newTestModel(t, func(root string) {})

	if m.sess.HasPendingWork() {
		t.Fatalf("two empty directories should leave no work outstanding, stats = %+v", m.sess.Stats())
	}

	tm, cmd := m.Update(spinnerTickMsg{})
	m = tm.(Model)
	if cmd != nil {
		t.Error("the spinner re-armed with nothing outstanding to animate")
	}
	if m.spinnerRunning {
		t.Error("spinnerRunning left true after the clock stopped; it would never restart")
	}
}

func TestSpinnerKeepsTickingWhileWorkIsOutstanding(t *testing.T) {
	// More compare jobs than the result channel can hold, and nothing
	// draining it: the queue stays non-empty for the whole test.
	m := newTestModel(t, func(root string) {
		for i := 0; i < 200; i++ {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d", i)), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	})

	if !m.sess.HasPendingWork() {
		t.Fatalf("the listing should have enqueued 200 comparisons, stats = %+v", m.sess.Stats())
	}

	tm, cmd := m.Update(spinnerTickMsg{})
	m = tm.(Model)
	if cmd == nil {
		t.Error("the spinner stopped while comparisons were still outstanding")
	}
	if !m.spinnerRunning {
		t.Error("spinnerRunning = false while a tick is scheduled")
	}
}

func TestSpinnerRestartsWhenNewWorkArrives(t *testing.T) {
	m := newTestModel(t, func(root string) {
		for i := 0; i < 200; i++ {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d", i)), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	})

	// Pretend the clock had stopped while the tree was idle.
	m.spinnerRunning = false

	if cmd := m.resumeSpinner(); cmd == nil {
		t.Fatal("resumeSpinner returned no tick despite outstanding work")
	}
	if !m.spinnerRunning {
		t.Fatal("resumeSpinner didn't mark the clock as running")
	}
	// Asking twice must not schedule a second, overlapping tick chain.
	if cmd := m.resumeSpinner(); cmd != nil {
		t.Error("resumeSpinner scheduled a second tick while one was already in flight")
	}
}

func TestKeypressWithoutWorkDoesNotStartTheSpinner(t *testing.T) {
	m := newTestModel(t, func(root string) {})
	m.spinnerRunning = false

	// 'r' only toggles a setting; it enqueues nothing.
	tm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = tm.(Model)
	if cmd != nil || m.spinnerRunning {
		t.Error("a keypress that enqueues no work started the animation clock")
	}
}
