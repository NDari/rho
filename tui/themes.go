package tui

import (
	"encoding/json"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/theme"
)

// bgQueryWait is how long Rho waits for the terminal to report its
// background before it paints its own.
const bgQueryWait = 500 * time.Millisecond

// themeKeys are the settings that name a theme.
var themeKeys = map[string]bool{"theme": true, "theme_dark": true, "theme_light": true}

// configuredTheme resolves the theme settings against the terminal's
// background.
func (m *Model) configuredTheme() (Theme, string) {
	cfg := m.agent.Config()
	return m.themes.Resolve(cfg.Theme, cfg.ThemeDark, cfg.ThemeLight, m.systemDark)
}

// applyConfiguredTheme switches to the theme the settings call for.
func (m *Model) applyConfiguredTheme() {
	t, _ := m.configuredTheme()
	if t.Name != m.theme.Name {
		m.setTheme(t)
		m.applyInputStyles()
	}
}

// themeFor is the theme shown when setting key is value.
func (m *Model) themeFor(key, value string) Theme {
	if key != "theme" {
		if t, ok := m.themes.Get(value); ok {
			return t
		}
		return m.theme
	}
	cfg := m.agent.Config()
	t, _ := m.themes.Resolve(value, cfg.ThemeDark, cfg.ThemeLight, m.systemDark)
	return t
}

// themePicker lists the themes for key ("theme", "theme_dark" or
// "theme_light") and shows each one as the cursor reaches it, as Vim's
// colorscheme preview does. Enter saves the choice; esc puts back the
// theme in use before. then, if set, runs after either (the /config list
// reopens itself this way).
func (m *Model) themePicker(key string, then func()) {
	before := m.theme
	s, _ := config.LookupSetting(key)
	cur := m.settingValue(s)
	names := m.themes.Names()
	if key == "theme" {
		names = append([]string{theme.System}, names...)
	}
	var items []pickerItem
	cursor := 0
	for i, n := range names {
		detail := ""
		if n == theme.System {
			detail = "theme_dark or theme_light, to match the terminal"
		}
		if n == cur {
			detail = "current"
			cursor = i
		}
		items = append(items, pickerItem{title: n, detail: detail, value: n})
	}
	p := newPicker(key, items, func(it pickerItem) tea.Cmd {
		cmd := m.setSetting(key, it.value.(string))
		if then != nil {
			then()
		}
		return cmd
	})
	p.cursor = cursor
	p.preview = true
	p.hint = "↑/↓ preview · enter save · type to filter · esc put back"
	p.onMove = func(it pickerItem) {
		m.setTheme(m.themeFor(key, it.value.(string)))
		m.applyInputStyles()
	}
	p.onCancel = func() {
		m.setTheme(before)
		m.applyInputStyles()
		if then != nil {
			then()
		}
	}
	m.picker = p
	p.onMove(items[cursor])
}

// themeSample is shown above the theme list when the transcript is
// empty, so a preview has something to color.
func themeSample() []item {
	args, _ := json.Marshal(map[string]string{"path": "greet.go"})
	return []item{
		&userItem{text: "Make the greeting friendlier."},
		&assistantItem{text: "## Greeting\n\nThe message comes from `greet`; here is the new version:\n\n" +
			"```go\n// greet returns a greeting for name.\nfunc greet(name string) string {\n\treturn fmt.Sprintf(\"Hello, %s!\", name)\n}\n```"},
		&toolItem{name: "edit", args: args, result: "edited greet.go", display: &events.Display{
			Kind: "diff", Path: "greet.go",
			Diff: &events.Diff{Added: 1, Removed: 1, Hunks: []events.Hunk{{OldStart: 3, NewStart: 3, Lines: []events.DiffLine{
				{Kind: events.LineContext, Text: "func greet(name string) string {"},
				{Kind: events.LineRemoved, Text: "\treturn \"hi \" + name"},
				{Kind: events.LineAdded, Text: "\treturn fmt.Sprintf(\"Hello, %s!\", name)"},
				{Kind: events.LineContext, Text: "}"},
			}}}},
		}},
		&noticeItem{level: "warn", text: "A warning looks like this."},
	}
}

// previewLines renders the transcript, or the sample when it has no
// conversation yet, for the area above a preview picker. sample reports
// the latter, which is shown from its start rather than its end.
func (m *Model) previewLines() (lines []string, sample bool) {
	for _, it := range m.items {
		switch it.(type) {
		case *userItem, *assistantItem, *toolItem:
			return m.lines, false
		}
	}
	rc := m.renderCtx()
	for i, it := range themeSample() {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, it.render(rc)...)
	}
	return lines, true
}
