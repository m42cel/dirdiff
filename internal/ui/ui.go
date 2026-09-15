// Package ui is the Bubble Tea presentation layer for dirdiff.
//
// This is currently a scaffold: it synchronously lists the top level of
// both directories and renders a static two-pane view to prove out the
// module structure and TUI dependencies. Background scanning, the
// priority queue, comparison levels, and reprioritization from SPEC.md
// are not implemented yet.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/m42cel/dirdiff/internal/diffmodel"
)

var (
	paneStyle = lipgloss.NewStyle().
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Underline(true)

	cursorStyle = lipgloss.NewStyle().
			Reverse(true)

	sameStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	differsStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	missingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// Model is the root Bubble Tea model.
type Model struct {
	leftRoot, rightRoot string
	entries             []diffmodel.Entry
	cursor              int
	width, height       int
	err                 error
}

// New builds the initial model for comparing leftRoot and rightRoot.
func New(leftRoot, rightRoot string) Model {
	return Model{leftRoot: leftRoot, rightRoot: rightRoot}
}

func (m Model) Init() tea.Cmd {
	return m.loadDir("")
}

// loadDir synchronously lists relDir under both roots and merges them.
// This stands in for the background listing scan until it's implemented.
func (m Model) loadDir(relDir string) tea.Cmd {
	return func() tea.Msg {
		entries, err := mergeListing(filepath.Join(m.leftRoot, relDir), filepath.Join(m.rightRoot, relDir))
		if err != nil {
			return errMsg{err}
		}
		return dirLoadedMsg{entries: entries}
	}
}

type dirLoadedMsg struct {
	entries []diffmodel.Entry
}

type errMsg struct{ err error }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case dirLoadedMsg:
		m.entries = msg.entries
		m.cursor = 0
	case errMsg:
		m.err = msg.err
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.err != nil {
		return fmt.Sprintf("error: %v\n", m.err)
	}

	paneWidth := m.width/2 - 2
	if paneWidth < 10 {
		paneWidth = 10
	}

	left := headerStyle.Render(m.leftRoot) + "\n"
	right := headerStyle.Render(m.rightRoot) + "\n"

	for i, e := range m.entries {
		line := renderRow(e)
		if i == m.cursor {
			line = cursorStyle.Render(line)
		}
		left += line + "\n"
		right += line + "\n"
	}

	leftPane := paneStyle.Width(paneWidth).Render(left)
	rightPane := paneStyle.Width(paneWidth).Render(right)

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
	footer := dimStyle.Render("↑/↓ move · q quit  (scaffold: existence only, no comparison levels yet)")

	return body + "\n" + footer
}

func renderRow(e diffmodel.Entry) string {
	glyph, style := statusGlyph(e)
	typeGlyph := "-"
	switch e.Type {
	case diffmodel.Dir:
		typeGlyph = "/"
	case diffmodel.Symlink:
		typeGlyph = "@"
	}
	return style.Render(glyph) + " " + e.Name + typeGlyph
}

func statusGlyph(e diffmodel.Entry) (string, lipgloss.Style) {
	switch e.Presence {
	case diffmodel.LeftOnly:
		return "→", missingStyle
	case diffmodel.RightOnly:
		return "←", missingStyle
	}
	switch e.Result {
	case diffmodel.Same:
		return "=", sameStyle
	case diffmodel.Differs:
		return "≠", differsStyle
	case diffmodel.CompareError:
		return "!", differsStyle
	default:
		return "·", dimStyle
	}
}

// entryKey identifies an entry by name and type, matching files and
// directories independently per SPEC.md §3.1.
type entryKey struct {
	name string
	typ  diffmodel.EntryType
}

// mergeListing reads leftDir and rightDir (either may not exist) and
// matches entries by (name, type) per SPEC.md §3.1, sorted with
// directories first, then alphabetically.
func mergeListing(leftDir, rightDir string) ([]diffmodel.Entry, error) {
	leftEntries, err := readDirEntries(leftDir)
	if err != nil {
		return nil, err
	}
	rightEntries, err := readDirEntries(rightDir)
	if err != nil {
		return nil, err
	}

	present := map[entryKey]diffmodel.Presence{}
	for k := range leftEntries {
		present[k] = diffmodel.LeftOnly
	}
	for k := range rightEntries {
		if _, ok := present[k]; ok {
			present[k] = diffmodel.Both
		} else {
			present[k] = diffmodel.RightOnly
		}
	}

	entries := make([]diffmodel.Entry, 0, len(present))
	for k, p := range present {
		entries = append(entries, diffmodel.Entry{
			Name:     k.name,
			Type:     k.typ,
			Presence: p,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type == diffmodel.Dir && entries[j].Type != diffmodel.Dir {
			return true
		}
		if entries[i].Type != diffmodel.Dir && entries[j].Type == diffmodel.Dir {
			return false
		}
		return entries[i].Name < entries[j].Name
	})

	return entries, nil
}

func readDirEntries(dir string) (map[entryKey]struct{}, error) {
	out := map[entryKey]struct{}{}

	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, de := range des {
		t := diffmodel.File
		if de.Type()&os.ModeSymlink != 0 {
			t = diffmodel.Symlink
		} else if de.IsDir() {
			t = diffmodel.Dir
		}
		out[entryKey{name: de.Name(), typ: t}] = struct{}{}
	}
	return out, nil
}
