package ui

import (
	"strings"
	"testing"
)

// The legend has to fit whatever width it's given, and a key must never
// be separated from what it does.
func TestHintLegendWrapsToWidthWithoutSplittingAnItem(t *testing.T) {
	for _, width := range []int{40, 60, 80, 100, 140, 400} {
		m, _ := newTestModel(t)
		m.width = width

		lines := m.statusHintLines()
		var seen []string
		for _, line := range lines {
			if got := len([]rune(line)); got > width {
				t.Errorf("width %d: line %q is %d wide", width, line, got)
			}
			seen = append(seen, strings.Split(line, itemSep)...)
		}
		if strings.Join(seen, "|") != strings.Join(hintItems, "|") {
			t.Errorf("width %d: items came back as %v; want every one of them, in order, intact", width, seen)
		}
	}
}

// The list area is sized against the status bar's height, so the two
// have to agree — including when a note or the selection prompt is
// displacing the legend, which must not change how tall the bar is.
func TestStatusBarRendersExactlyTheHeightTheLayoutAssumes(t *testing.T) {
	cases := []struct {
		name  string
		setup func(Model) Model
	}{
		{"legend", func(m Model) Model { return m }},
		{"selection prompt", func(m Model) Model { return press(t, m, "s") }},
		{"note", func(m Model) Model {
			m = press(t, m, "s")
			return press(t, m, " ") // nothing selectable here: sets a note
		}},
	}
	for _, c := range cases {
		for _, width := range []int{40, 80, 140} {
			m, _ := newTestModel(t)
			m.width = width
			m = c.setup(m)

			got := len(strings.Split(m.renderStatusBar(), "\n"))
			if got != m.statusBarHeight() {
				t.Errorf("%s at width %d: rendered %d lines, layout reserves %d", c.name, width, got, m.statusBarHeight())
			}
		}
	}
}

// Narrower terminals leave less room for rows, and the whole frame still
// has to fit the window it was given.
func TestListAreaShrinksAsTheStatusBarWraps(t *testing.T) {
	m, _ := newTestModel(t)
	m.height = 40

	m.width = 400
	wide := m.listAreaHeight()
	m.width = 40
	narrow := m.listAreaHeight()

	if narrow >= wide {
		t.Errorf("list area = %d rows at width 40 and %d at 400; want the wrapped legend to cost rows", narrow, wide)
	}
	if total := narrow + paneBoxOverhead + paneTitleRows + detailsPanelHeight + m.statusBarHeight(); total != m.height {
		t.Errorf("the frame adds up to %d rows in a %d-row window", total, m.height)
	}
}
