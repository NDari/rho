package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/NDari/rho/core/config"
)

// configPicker lists the changeable settings. Enter moves the selected
// setting to its next choice, applies it and saves it, leaving the list
// open.
func (m *Model) configPicker() {
	p := newPicker("Config", m.configItems(), nil)
	p.hint = "↑/↓ move · enter change · type to filter · esc close"
	p.keys = map[string]func(pickerItem) tea.Cmd{
		"enter": func(it pickerItem) tea.Cmd {
			key := it.value.(string)
			s, _ := config.LookupSetting(key)
			next := config.Next(m.settingChoices(s), s.Value(m.agent.Config()), s.Int)
			cmd := m.setSetting(key, next)
			p.items = m.configItems()
			p.refilter()
			return cmd
		},
	}
	m.picker = p
}

func (m *Model) configItems() []pickerItem {
	cfg := m.agent.Config()
	keyW, valW := 0, 0
	for _, s := range config.Settings {
		keyW = max(keyW, len(s.Key))
		valW = max(valW, len(s.Value(cfg)))
	}
	items := make([]pickerItem, 0, len(config.Settings))
	for _, s := range config.Settings {
		title := fmt.Sprintf("%-*s  %-*s", keyW, s.Key, valW, s.Value(cfg))
		items = append(items, pickerItem{title: title, detail: s.Description, value: s.Key})
	}
	return items
}

// settingChoices are the values Enter cycles through.
func (m *Model) settingChoices(s config.Setting) []string {
	switch s.Key {
	case "theme":
		return ThemeNames(m.themes)
	case "default_model":
		var ids []string
		for _, md := range m.app.Config.Models {
			ids = append(ids, md.ID)
		}
		return ids
	}
	return s.Choices
}

// setSetting applies a setting for this run and saves it to the global
// config.
func (m *Model) setSetting(key, value string) tea.Cmd {
	var theme Theme
	if key == "theme" {
		t, ok := m.themes[value]
		if !ok {
			m.settingError("unknown theme " + value)
			return nil
		}
		theme = t
	}
	note, err := m.app.SetSetting(key, value)
	if err != nil {
		m.settingError(err.Error())
		return nil
	}
	if key == "theme" {
		m.setTheme(theme)
		m.applyInputStyles()
	}
	s, _ := config.LookupSetting(key)
	m.setFlash(key + " = " + s.Value(m.agent.Config()) + " (saved)")
	if note != "" {
		m.notice("warn", note)
	}
	m.dirty = true
	return nil
}

// settingError shows err both now (the picker may cover the transcript)
// and in the transcript.
func (m *Model) settingError(err string) {
	m.setFlash(err)
	m.notice("error", err)
}

// configCommand handles "/config" and "/config KEY VALUE".
func (m *Model) configCommand(args string) tea.Cmd {
	if args == "" {
		m.configPicker()
		return nil
	}
	key, value, _ := strings.Cut(args, " ")
	if _, ok := config.LookupSetting(key); !ok {
		var keys []string
		for _, s := range config.Settings {
			keys = append(keys, s.Key)
		}
		m.notice("error", "unknown setting "+key+"; one of: "+strings.Join(keys, ", "))
		return nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		m.notice("error", "usage: /config "+key+" VALUE (or /config to pick)")
		return nil
	}
	return m.setSetting(key, value)
}
