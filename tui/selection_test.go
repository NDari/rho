package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/NDari/rho/core/app"
)

func TestMouseSelectionCopies(t *testing.T) {
	t.Setenv("RHO_HOME", t.TempDir())
	a, err := app.New(app.Options{Cwd: t.TempDir(), Model: "openai/test-model", NoExtensions: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Agent.Session().Close() })
	m := New(a)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m.rebuildLines()
	m.lines = []string{"\x1b[1mhello world\x1b[0m   ", "second line", "third"}
	m.dirty = false
	m.offset, m.follow = 0, false

	m.Update(tea.MouseClickMsg{X: 6, Y: 0, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: 3, Y: 2, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: 3, Y: 2, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("release did not copy")
	}
	if got, want := m.selectedText(), "world\nsecond line\nthir"; got != want {
		t.Errorf("selected %q, want %q", got, want)
	}
	body := m.highlightSelection([]string{m.lines[0], m.lines[1], m.lines[2]}, 0)
	if body[1] == m.lines[1] {
		t.Error("selection not highlighted")
	}

	// A click without a drag copies nothing and clears the selection.
	m.Update(tea.MouseClickMsg{X: 2, Y: 1, Button: tea.MouseLeft})
	if _, cmd := m.Update(tea.MouseReleaseMsg{X: 2, Y: 1, Button: tea.MouseLeft}); cmd != nil || m.sel != nil {
		t.Error("a plain click copied or kept a selection")
	}

	// Off: the mouse does nothing.
	off := false
	m.app.Config.TUI.MouseSelect = &off
	m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: 4, Y: 0, Button: tea.MouseLeft})
	if _, cmd := m.Update(tea.MouseReleaseMsg{X: 4, Y: 0, Button: tea.MouseLeft}); cmd != nil || m.sel != nil {
		t.Error("selection worked with mouse_select off")
	}
}
