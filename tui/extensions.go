package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/NDari/rho/core/ext"
)

// extensionsPicker lists the extensions. Enter turns the selected one on
// or off; tab shows the full report.
func (m *Model) extensionsPicker() {
	items := m.extensionItems()
	if len(items) == 0 {
		m.addItem(&textItem{title: "Extensions", body: m.ext.Describe()})
		return
	}
	p := newPicker("Extensions", items, nil)
	p.hint = "↑/↓ move · enter on/off · tab details · type to filter · esc close"
	busy := false
	p.keys = map[string]func(pickerItem) tea.Cmd{
		"enter": func(it pickerItem) tea.Cmd {
			if busy {
				return nil
			}
			x := it.value.(ExtensionInfo)
			on := x.Disabled
			busy = true
			verb := "stopping"
			if on {
				verb = "starting"
			}
			m.setFlash(verb + " " + x.Name + "…")
			var note string
			return m.async(func() error {
				var err error
				note, err = m.ext.SetEnabled(context.Background(), x.ID, on)
				return err
			}, func(m *Model) tea.Cmd {
				busy = false
				if m.picker == p {
					p.items = m.extensionItems()
					p.refilter()
				}
				state := "off"
				if on {
					state = "on"
				}
				m.setFlash(x.Name + " " + state + " (saved)")
				if note != "" {
					m.notice("warn", note)
				}
				return nil
			})
		},
		"tab": func(pickerItem) tea.Cmd {
			m.picker = nil
			m.addItem(&textItem{title: "Extensions", body: m.ext.Describe()})
			return nil
		},
	}
	m.picker = p
}

func (m *Model) extensionItems() []pickerItem {
	list := m.ext.List()
	nameW, statusW := 0, 0
	for _, x := range list {
		nameW = max(nameW, len(x.Name))
		statusW = max(statusW, len(extStatus(x)))
	}
	items := make([]pickerItem, 0, len(list))
	for _, x := range list {
		scope := "global"
		if x.Project {
			scope = "project"
		}
		detail := x.Kind + ", " + scope
		var parts []string
		if x.Tools > 0 {
			parts = append(parts, plural(x.Tools, "tool"))
		}
		if x.Commands > 0 {
			parts = append(parts, plural(x.Commands, "command"))
		}
		if x.Hooks > 0 {
			parts = append(parts, plural(x.Hooks, "hook"))
		}
		if len(parts) > 0 {
			detail += " · " + strings.Join(parts, ", ")
		}
		if x.Err != "" {
			detail += " · " + x.Err
		}
		items = append(items, pickerItem{title: fmt.Sprintf("%-*s  %-*s", nameW, x.Name, statusW, extStatus(x)), detail: detail, value: x})
	}
	return items
}

// extStatus is "on", "off", or what went wrong (failed, not approved...).
func extStatus(x ExtensionInfo) string {
	switch {
	case x.Disabled:
		return "off"
	case x.Status == ext.StatusRunning:
		return "on"
	}
	return x.Status
}
