package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// textPos is a cell in the transcript: a line index into m.lines and a
// column in cells.
type textPos struct{ line, col int }

func (a textPos) before(b textPos) bool {
	return a.line < b.line || a.line == b.line && a.col < b.col
}

// selection is a mouse selection in the transcript, from where the button
// went down (anchor) to where it is now (head).
type selection struct {
	anchor, head textPos
	dragging     bool
}

// ordered returns the selection's start and end, end exclusive.
func (s *selection) ordered() (textPos, textPos) {
	a, b := s.anchor, s.head
	if b.before(a) {
		a, b = b, a
	}
	b.col++ // include the cell under the pointer
	return a, b
}

func (s *selection) empty() bool { return s.anchor == s.head }

// mouseSelectOn reports whether drag-to-select is enabled and possible.
func (m *Model) mouseSelectOn() bool {
	return m.app.Config.TUI.MouseSelectEnabled() && m.picker == nil && m.dlg == nil
}

// transcriptPos maps a screen cell to a transcript position, clamping to
// the visible transcript.
func (m *Model) transcriptPos(x, y int) textPos {
	y = min(max(0, y), m.transcriptHeight()-1)
	return textPos{line: m.offset + y, col: max(0, x)}
}

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if !m.mouseSelectOn() {
		m.sel = nil
		return nil
	}
	mouse := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		m.sel = nil
		if mouse.Button != tea.MouseLeft || mouse.Y >= m.transcriptHeight() {
			return nil
		}
		if m.dirty {
			m.rebuildLines()
		}
		p := m.transcriptPos(mouse.X, mouse.Y)
		m.sel = &selection{anchor: p, head: p, dragging: true}
	case tea.MouseMotionMsg:
		if m.sel == nil || !m.sel.dragging {
			return nil
		}
		// Dragging up to the top row, or below the transcript, scrolls.
		if mouse.Y <= 0 && m.sel.anchor.line > m.offset {
			m.scroll(-1)
		} else if mouse.Y >= m.transcriptHeight() {
			m.scroll(1)
		}
		m.sel.head = m.transcriptPos(mouse.X, mouse.Y)
	case tea.MouseReleaseMsg:
		if m.sel == nil || !m.sel.dragging {
			return nil
		}
		m.sel.dragging = false
		m.sel.head = m.transcriptPos(mouse.X, mouse.Y)
		if m.sel.empty() {
			m.sel = nil // a plain click
			return nil
		}
		text := m.selectedText()
		if text == "" {
			m.sel = nil
			return nil
		}
		m.setFlash(fmt.Sprintf("copied %s", plural(len([]rune(text)), "character")))
		return tea.Batch(tea.SetClipboard(text), tea.SetPrimaryClipboard(text))
	}
	return nil
}

// selectedText is the plain text of the selection, with trailing spaces
// trimmed from each line.
func (m *Model) selectedText() string {
	start, end := m.sel.ordered()
	var out []string
	for i := start.line; i <= end.line && i < len(m.lines); i++ {
		from, to := 0, ansi.StringWidth(m.lines[i])
		if i == start.line {
			from = start.col
		}
		if i == end.line {
			to = min(to, end.col)
		}
		plain := ""
		if from < to {
			plain = ansi.Strip(ansi.Cut(m.lines[i], from, to))
		}
		out = append(out, strings.TrimRight(plain, " "))
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// highlightSelection shows the selection in the visible lines, which
// start at transcript line first.
func (m *Model) highlightSelection(body []string, first int) []string {
	if m.sel == nil || m.sel.empty() {
		return body
	}
	start, end := m.sel.ordered()
	style := lipgloss.NewStyle().Reverse(true)
	for row := range body {
		i := first + row
		if i < start.line || i > end.line || i >= len(m.lines) {
			continue
		}
		line := body[row]
		w := ansi.StringWidth(line)
		from, to := 0, w
		if i == start.line {
			from = start.col
		}
		if i == end.line {
			to = min(to, end.col)
		}
		if from >= to {
			continue
		}
		body[row] = ansi.Cut(line, 0, from) + style.Render(ansi.Strip(ansi.Cut(line, from, to))) + ansi.Cut(line, to, w)
	}
	return body
}
