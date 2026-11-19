package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// pickerItem is one selectable row.
type pickerItem struct {
	title  string
	detail string
	value  any
	depth  int
}

// picker is a filterable list overlay.
type picker struct {
	title    string
	hint     string
	items    []pickerItem
	filter   string
	filtered []int
	cursor   int
	onSelect func(pickerItem) tea.Cmd
	// keys maps extra keys (e.g. "ctrl+l") to actions on the current item.
	keys map[string]func(pickerItem) tea.Cmd
}

func newPicker(title string, items []pickerItem, onSelect func(pickerItem) tea.Cmd) *picker {
	p := &picker{title: title, items: items, onSelect: onSelect, hint: "↑/↓ move · enter select · type to filter · esc close"}
	p.refilter()
	return p
}

func (p *picker) refilter() {
	p.filtered = p.filtered[:0]
	f := strings.ToLower(p.filter)
	for i, it := range p.items {
		if f == "" || strings.Contains(strings.ToLower(it.title+" "+it.detail), f) {
			p.filtered = append(p.filtered, i)
		}
	}
	if p.cursor >= len(p.filtered) {
		p.cursor = max(0, len(p.filtered)-1)
	}
}

func (p *picker) current() (pickerItem, bool) {
	if len(p.filtered) == 0 {
		return pickerItem{}, false
	}
	return p.items[p.filtered[p.cursor]], true
}

// update handles a key. It returns done=true when the picker should close.
func (p *picker) update(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	key := msg.String()
	if f, ok := p.keys[key]; ok {
		if it, ok := p.current(); ok {
			return f(it), false
		}
		return nil, false
	}
	switch key {
	case "esc", "ctrl+c":
		return nil, true
	case "up", "ctrl+p":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "ctrl+n":
		if p.cursor < len(p.filtered)-1 {
			p.cursor++
		}
	case "pgup":
		p.cursor = max(0, p.cursor-10)
	case "pgdown":
		p.cursor = min(len(p.filtered)-1, p.cursor+10)
	case "home":
		p.cursor = 0
	case "end":
		p.cursor = max(0, len(p.filtered)-1)
	case "enter":
		if it, ok := p.current(); ok && p.onSelect != nil {
			return p.onSelect(it), true
		}
		return nil, true
	case "backspace":
		if p.filter != "" {
			r := []rune(p.filter)
			p.filter = string(r[:len(r)-1])
			p.refilter()
		}
	default:
		if t := msg.Text; t != "" {
			p.filter += t
			p.refilter()
		}
	}
	return nil, false
}

func (p *picker) render(rc *renderCtx, height int) []string {
	out := []string{rc.st.title.Render(p.title)}
	filter := p.filter
	if filter == "" {
		filter = rc.st.subtle.Render("type to filter")
	}
	out = append(out, rc.st.muted.Render("› ")+filter, "")
	listH := max(1, height-len(out)-2)
	start := 0
	if p.cursor >= listH {
		start = p.cursor - listH + 1
	}
	for i := start; i < len(p.filtered) && i < start+listH; i++ {
		it := p.items[p.filtered[i]]
		prefix := "  "
		// Shorten the title before the detail, so markers such as
		// "● current" stay visible next to long titles.
		avail := max(0, rc.width-3)
		detail := ""
		if it.detail != "" {
			detail = ansi.Truncate(it.detail, avail/2, "…")
		}
		titleW := avail
		if detail != "" {
			titleW = max(0, avail-2-ansi.StringWidth(detail))
		}
		title := ansi.Truncate(strings.Repeat("  ", it.depth)+it.title, titleW, "…")
		line := title
		if i == p.cursor {
			prefix = rc.st.accent.Render("▸ ")
			line = rc.st.selected.Render(title)
		}
		if detail != "" {
			line += "  " + rc.st.muted.Render(detail)
		}
		out = append(out, prefix+line)
	}
	if len(p.filtered) == 0 {
		out = append(out, rc.st.muted.Render("  (no matches)"))
	}
	for len(out) < height-1 {
		out = append(out, "")
	}
	count := fmt.Sprintf("%d/%d", len(p.filtered), len(p.items))
	out = append(out, rc.st.subtle.Render(p.hint+" · "+count))
	return out
}
