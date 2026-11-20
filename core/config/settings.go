package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Kind is the type of a setting's value.
type Kind int

// Setting kinds.
const (
	KindString Kind = iota
	KindInt
	KindBool
)

// Setting is a config key that can be changed while Rho runs (the TUI's
// /config). Choices are offered in order; a value outside them is allowed.
type Setting struct {
	Key         string
	Description string
	Kind        Kind
	// PerModel settings belong to the current model and are saved in its
	// [[models]] entry.
	PerModel bool
	// Optional settings accept "" (shown as "default"), which removes the
	// key so the built-in behavior applies.
	Optional bool
	Choices  []string
	get      func(c Config, model string) string
	set      func(c *Config, model, v string)
}

// Settings lists the changeable settings. default_model and theme take
// their choices from the configured models and themes, so they have none
// here.
var Settings = []Setting{
	{Key: "default_model", Description: "model for new sessions",
		get: func(c Config, _ string) string { return c.DefaultModel },
		set: func(c *Config, _, v string) { c.DefaultModel = v }},
	{Key: "reasoning_effort", Description: "how hard the model thinks", PerModel: true, Optional: true,
		Choices: []string{"", "low", "medium", "high"},
		get: func(c Config, model string) string {
			m, _ := c.ResolveModel(model)
			return m.ReasoningEffort
		},
		set: func(c *Config, model, v string) { c.modelEntry(model).ReasoningEffort = v }},
	{Key: "theme", Description: "color theme",
		get: func(c Config, _ string) string { return c.Theme },
		set: func(c *Config, _, v string) { c.Theme = v }},
	boolSetting("tui.show_thinking", "show thinking expanded (ctrl+t toggles)",
		func(c *Config) bool { return c.TUI.ShowThinking }, func(c *Config, b bool) { c.TUI.ShowThinking = b }),
	boolSetting("tui.mouse_select", "drag to select text and copy it (off: the terminal's own selection)",
		func(c *Config) bool { return c.TUI.MouseSelectEnabled() }, func(c *Config, b bool) { c.TUI.MouseSelect = &b }),
	intSetting("tui.side_by_side_width", "terminal width for side-by-side diffs (0: never)", []string{"0", "120", "140", "160", "200"},
		func(c *Config) *int { return &c.TUI.SideBySideWidth }),
	boolSetting("compaction.auto", "compact automatically near the context limit",
		func(c *Config) bool { return c.Compaction.AutoEnabled() }, func(c *Config, b bool) { c.Compaction.Auto = &b }),
	intSetting("compaction.reserve", "tokens kept free below the context window", []string{"8000", "16000", "32000", "64000"},
		func(c *Config) *int { return &c.Compaction.Reserve }),
	intSetting("compaction.keep_recent", "recent tokens kept verbatim when compacting", []string{"10000", "20000", "40000", "80000"},
		func(c *Config) *int { return &c.Compaction.KeepRecent }),
	intSetting("max_retries", "retries for transient provider errors", []string{"0", "1", "2", "3", "5", "10"},
		func(c *Config) *int { return &c.MaxRetries }),
	intSetting("bash.timeout", "default bash timeout in seconds", []string{"30", "60", "120", "300", "600"},
		func(c *Config) *int { return &c.Bash.Timeout }),
	intSetting("bash.max_timeout", "longest bash timeout the model may ask for, in seconds", []string{"300", "600", "1200", "1800", "3600"},
		func(c *Config) *int { return &c.Bash.MaxTimeout }),
	boolSetting("update_check", "look for new Rho releases once a day",
		func(c *Config) bool { return c.UpdateCheckEnabled() }, func(c *Config, b bool) { c.UpdateCheck = &b }),
}

func intSetting(key, desc string, choices []string, field func(*Config) *int) Setting {
	return Setting{
		Key: key, Description: desc, Kind: KindInt, Choices: choices,
		get: func(c Config, _ string) string { return strconv.Itoa(*field(&c)) },
		set: func(c *Config, _, v string) { n, _ := strconv.Atoi(v); *field(c) = n },
	}
}

func boolSetting(key, desc string, get func(*Config) bool, set func(*Config, bool)) Setting {
	return Setting{
		Key: key, Description: desc, Kind: KindBool, Choices: []string{"true", "false"},
		get: func(c Config, _ string) string { return strconv.FormatBool(get(&c)) },
		set: func(c *Config, _, v string) { set(c, v == "true") },
	}
}

// modelEntry returns the [[models]] entry for id, adding one for an ad-hoc
// "provider/name" model. c.Models is copied first, since Config values
// share it.
func (c *Config) modelEntry(id string) *Model {
	c.Models = slices.Clone(c.Models)
	for i := range c.Models {
		if c.Models[i].ID == id {
			return &c.Models[i]
		}
	}
	m, _ := c.ResolveModel(id)
	c.Models = append(c.Models, m)
	return &c.Models[len(c.Models)-1]
}

// LookupSetting finds a changeable setting by key.
func LookupSetting(key string) (Setting, bool) {
	for _, s := range Settings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// Value returns the setting's current value in c. model is the current
// model's id, used by per-model settings.
func (s Setting) Value(c Config, model string) string { return s.get(c, model) }

// Apply returns c with the setting changed to value, after checking it.
// For an optional setting, "" or "default" restores the default.
func (s Setting) Apply(c Config, model, value string) (Config, error) {
	value = strings.TrimSpace(value)
	if s.Optional && (value == "" || value == "default") {
		s.set(&c, model, "")
		return c, nil
	}
	switch s.Kind {
	case KindInt:
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return c, fmt.Errorf("%s must be a whole number of 0 or more", s.Key)
		}
		value = strconv.Itoa(n)
	case KindBool:
		switch strings.ToLower(value) {
		case "true", "on", "yes":
			value = "true"
		case "false", "off", "no":
			value = "false"
		default:
			return c, fmt.Errorf("%s must be true or false", s.Key)
		}
	default:
		if value == "" {
			return c, fmt.Errorf("%s must not be empty", s.Key)
		}
	}
	if s.Key == "default_model" {
		if _, err := c.ResolveModel(value); err != nil {
			return c, err
		}
	}
	if s.PerModel {
		if _, err := c.ResolveModel(model); err != nil {
			return c, err
		}
	}
	s.set(&c, model, value)
	return c, nil
}

// Next returns the choice after the current value, wrapping around. A
// value that isn't a choice moves to the first choice after it (by number
// for integer settings).
func Next(choices []string, current string, isInt bool) string {
	if len(choices) == 0 {
		return current
	}
	for i, c := range choices {
		if c == current {
			return choices[(i+1)%len(choices)]
		}
	}
	if cur, err := strconv.Atoi(current); isInt && err == nil {
		for _, c := range choices {
			if n, _ := strconv.Atoi(c); n > cur {
				return c
			}
		}
	}
	return choices[0]
}

// ErrModelNotInFile is returned when a per-model setting can't be saved
// because the file has no [[models]] entry for the model.
var ErrModelNotInFile = errors.New("the model has no [[models]] entry in the file")

// SaveSetting writes the setting's value into the TOML file at path,
// creating the file if needed. A per-model setting goes into the [[models]]
// entry with id model. An empty optional value removes the key. The rest of
// the file, comments included, is kept as is.
func SaveSetting(path string, s Setting, model, value string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var v any = value
	switch {
	case s.Optional && value == "":
		v = nil
	case s.Kind == KindInt:
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Key, err)
		}
		v = n
	case s.Kind == KindBool:
		v = value == "true"
	}
	var out string
	if s.PerModel {
		out, err = setModelKey(string(data), model, s.Key, v)
	} else {
		out, err = setKey(string(data), s.Key, v)
	}
	if errors.Is(err, ErrModelNotInFile) {
		return err
	}
	if err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	return writeFile(path, out)
}

// writeFile replaces path atomically, keeping its permissions.
func writeFile(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// section is a part of a TOML file: the root (before any header), a
// [table] or one [[array]] entry. Body lines are [body, end).
type section struct {
	name      string
	array     bool
	body, end int
}

func sections(lines []string) []section {
	secs := []section{{body: 0, end: len(lines)}}
	for i, l := range lines {
		name, array, ok := header(l)
		if !ok {
			continue
		}
		secs[len(secs)-1].end = i
		secs = append(secs, section{name: name, array: array, body: i + 1, end: len(lines)})
	}
	return secs
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// setKey sets a key ("name" or "table.name") in TOML text, or removes it
// when value is nil. It edits the existing line when there is one, or adds
// the key, and checks that the result parses with the new value.
func setKey(text, key string, value any) (string, error) {
	table, name, dotted := strings.Cut(key, ".")
	if !dotted {
		table, name = "", key
	}
	lines := splitLines(text)
	for _, sec := range sections(lines) {
		if sec.name == table && !sec.array {
			out, err := editSection(lines, sec, name, value)
			if err != nil {
				return "", err
			}
			return checked(joinLines(out), key, value, func(m map[string]any) any { return m })
		}
	}
	if value == nil {
		return text, nil
	}
	val, err := tomlValue(value)
	if err != nil {
		return "", err
	}
	// No such table yet: add it at the end.
	out := strings.TrimRight(text, "\n")
	if out != "" {
		out += "\n\n"
	}
	return checked(out+"["+table+"]\n"+name+" = "+val+"\n", key, value, func(m map[string]any) any { return m })
}

// setModelKey sets (or with a nil value removes) key in the [[models]]
// entry whose id is model.
func setModelKey(text, model, key string, value any) (string, error) {
	lines := splitLines(text)
	for _, sec := range sections(lines) {
		if !sec.array || sec.name != "models" || sectionID(lines, sec) != model {
			continue
		}
		out, err := editSection(lines, sec, key, value)
		if err != nil {
			return "", err
		}
		return checked(joinLines(out), key, value, func(m map[string]any) any {
			list, _ := m["models"].([]map[string]any)
			for _, e := range list {
				if e["id"] == model {
					return e
				}
			}
			return nil
		})
	}
	return "", ErrModelNotInFile
}

// sectionID returns the string value of the section's id key.
func sectionID(lines []string, sec section) string {
	for _, l := range lines[sec.body:sec.end] {
		if k, _, ok := keyLine(l); ok && k == "id" {
			var v struct{ ID string }
			if _, err := toml.Decode("id = "+strings.TrimSpace(strings.SplitN(l, "=", 2)[1]), &v); err == nil {
				return v.ID
			}
		}
	}
	return ""
}

// editSection replaces, adds or (for a nil value) removes name in sec.
func editSection(lines []string, sec section, name string, value any) ([]string, error) {
	val := ""
	if value != nil {
		var err error
		if val, err = tomlValue(value); err != nil {
			return nil, err
		}
	}
	out := slices.Clone(lines)
	for i := sec.body; i < sec.end; i++ {
		k, rest, ok := keyLine(out[i])
		if !ok || k != name {
			continue
		}
		// The value may continue on later lines (a multi-line array).
		last, col := valueEnd(out, i, len(out[i])-len(rest))
		if value == nil {
			return slices.Delete(out, i, last+1), nil
		}
		comment := ""
		if c := strings.Index(out[last][col:], "#"); c != -1 {
			comment = " " + out[last][col+c:]
		}
		out[i] = out[i][:len(out[i])-len(rest)] + val + comment
		return slices.Delete(out, i+1, last+1), nil
	}
	if value == nil {
		return out, nil
	}
	line := name + " = " + val
	// Insert after the last non-blank line of the section.
	blank := func(i int) bool { return strings.TrimSpace(out[i]) == "" }
	at := sec.end
	for at > sec.body && blank(at-1) {
		at--
	}
	// A comment block set off by a blank line describes what follows the
	// section (the next header), so insert above it.
	j := at
	for j > sec.body && strings.HasPrefix(strings.TrimSpace(out[j-1]), "#") {
		j--
	}
	if j < at && j > sec.body && blank(j-1) {
		at = j
		for at > sec.body && blank(at-1) {
			at--
		}
	}
	ins := []string{line}
	if sec.name == "" && at == 0 && at < len(out) {
		// A first line right before a header: keep them apart.
		ins = append(ins, "")
	}
	return slices.Insert(out, at, ins...), nil
}

// header reports the table name of a "[name]" or "[[name]]" line.
func header(l string) (name string, array, ok bool) {
	t := strings.TrimSpace(l)
	if !strings.HasPrefix(t, "[") {
		return "", false, false
	}
	if i := strings.Index(t, "#"); i != -1 {
		t = strings.TrimSpace(t[:i])
	}
	array = strings.HasPrefix(t, "[[")
	t = strings.TrimSuffix(strings.TrimPrefix(t, "["), "]")
	t = strings.TrimSuffix(strings.TrimPrefix(t, "["), "]")
	return strings.TrimSpace(t), array, true
}

// keyLine splits "  key = value # note" into the key and the value text
// (from the first non-blank character after "=").
func keyLine(l string) (key, rest string, ok bool) {
	k, rest, ok := strings.Cut(l, "=")
	if !ok {
		return "", "", false
	}
	k = strings.Trim(strings.TrimSpace(k), `"'`)
	if k == "" || strings.HasPrefix(k, "#") {
		return "", "", false
	}
	return k, strings.TrimLeft(rest, " \t"), true
}

// valueEnd finds where the value starting at lines[i][col] ends: the line
// and the column just after it. Strings, arrays and inline tables are
// followed across lines; anything else ends at a comment or the line end.
func valueEnd(lines []string, i, col int) (int, int) {
	depth := 0
	var quote byte
	for ; i < len(lines); i, col = i+1, 0 {
		l := lines[i]
		for ; col < len(l); col++ {
			ch := l[col]
			switch {
			case quote != 0:
				if ch == '\\' && quote == '"' {
					col++
				} else if ch == quote {
					quote = 0
					if depth == 0 {
						return i, col + 1
					}
				}
			case ch == '"' || ch == '\'':
				quote = ch
			case ch == '#':
				if depth == 0 {
					return i, col
				}
				col = len(l)
			case ch == '[' || ch == '{':
				depth++
			case ch == ']' || ch == '}':
				depth--
				if depth == 0 {
					return i, col + 1
				}
			}
		}
		if depth == 0 && quote == 0 {
			return i, len(l)
		}
		quote = 0 // strings don't span lines here
	}
	return len(lines) - 1, len(lines[len(lines)-1])
}

func tomlValue(v any) (string, error) {
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(map[string]any{"v": v}); err != nil {
		return "", err
	}
	_, val, _ := strings.Cut(strings.TrimSpace(b.String()), "= ")
	return val, nil
}

// checked parses text and confirms key now holds want (or is gone, for a
// nil want) in the table that table picks, so an unusual layout (an inline
// table, a dotted key) is refused instead of written wrongly.
func checked(text, key string, want any, table func(map[string]any) any) (string, error) {
	var m map[string]any
	if _, err := toml.Decode(text, &m); err != nil {
		return "", fmt.Errorf("could not set %s: %w", key, err)
	}
	got := table(m)
	for _, part := range strings.Split(key, ".") {
		mm, ok := got.(map[string]any)
		if !ok {
			got = nil
			break
		}
		got = mm[part]
	}
	if !reflect.DeepEqual(got, normalize(want)) {
		return "", fmt.Errorf("could not set %s automatically; edit the file by hand", key)
	}
	return text, nil
}

// normalize converts want to the types the TOML decoder produces.
func normalize(v any) any {
	switch v := v.(type) {
	case int:
		return int64(v)
	case []string:
		out := make([]any, len(v))
		for i, s := range v {
			out[i] = s
		}
		return out
	}
	return v
}

// SetExtensionDisabled adds name to (or removes it from)
// extensions.disabled in the TOML file at path.
func SetExtensionDisabled(path, name string, disabled bool) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var file Config
	if _, err := toml.Decode(string(data), &file); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if slices.Contains(file.Extensions.Disabled, name) == disabled {
		return nil // already so
	}
	list := slices.DeleteFunc(file.Extensions.Disabled, func(n string) bool { return n == name })
	if disabled {
		list = append(list, name)
	}
	if list == nil {
		list = []string{}
	}
	out, err := setKey(string(data), "extensions.disabled", list)
	if err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	return writeFile(path, out)
}

// GlobalFile is the global config file.
func (p Paths) GlobalFile() string { return filepath.Join(p.Home, "config.toml") }

// ProjectSets reports whether the project config sets key (for a
// per-model setting, defines the model at all), which then overrides the
// global value.
func (p Paths) ProjectSets(s Setting, model string) bool {
	if p.ProjectDir == "" || p.ProjectDir == p.Home {
		return false
	}
	var c Config
	md, err := toml.DecodeFile(filepath.Join(p.ProjectDir, "config.toml"), &c)
	if err != nil {
		return false
	}
	if s.PerModel {
		return slices.ContainsFunc(c.Models, func(m Model) bool { return m.ID == model })
	}
	return md.IsDefined(strings.Split(s.Key, ".")...)
}

// ProjectDisables reports whether the project config lists extension name
// in extensions.disabled.
func (p Paths) ProjectDisables(name string) bool {
	if p.ProjectDir == "" || p.ProjectDir == p.Home {
		return false
	}
	var c Config
	if _, err := toml.DecodeFile(filepath.Join(p.ProjectDir, "config.toml"), &c); err != nil {
		return false
	}
	return slices.Contains(c.Extensions.Disabled, name)
}
