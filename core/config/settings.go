package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Setting is a config key that can be changed while Rho runs (the TUI's
// /config). Choices are offered in order; a value outside them is allowed.
type Setting struct {
	Key         string
	Description string
	// Int is true for integer settings; others are strings.
	Int     bool
	Choices []string
	get     func(Config) string
	set     func(*Config, string)
}

// Settings lists the changeable settings. default_model and theme take
// their choices from the configured models and themes, so they have none
// here.
var Settings = []Setting{
	{Key: "default_model", Description: "model for new sessions",
		get: func(c Config) string { return c.DefaultModel }, set: func(c *Config, v string) { c.DefaultModel = v }},
	{Key: "theme", Description: "color theme",
		get: func(c Config) string { return c.Theme }, set: func(c *Config, v string) { c.Theme = v }},
	intSetting("max_retries", "retries for transient provider errors", []string{"0", "1", "2", "3", "5", "10"},
		func(c *Config) *int { return &c.MaxRetries }),
	intSetting("bash.timeout", "default bash timeout in seconds", []string{"30", "60", "120", "300", "600"},
		func(c *Config) *int { return &c.Bash.Timeout }),
	intSetting("bash.max_timeout", "longest bash timeout the model may ask for, in seconds", []string{"300", "600", "1200", "1800", "3600"},
		func(c *Config) *int { return &c.Bash.MaxTimeout }),
	intSetting("compaction.reserve", "tokens kept free below the context window", []string{"8000", "16000", "32000", "64000"},
		func(c *Config) *int { return &c.Compaction.Reserve }),
	intSetting("compaction.keep_recent", "recent tokens kept verbatim when compacting", []string{"10000", "20000", "40000", "80000"},
		func(c *Config) *int { return &c.Compaction.KeepRecent }),
	intSetting("tui.side_by_side_width", "terminal width for side-by-side diffs (0: never)", []string{"0", "120", "140", "160", "200"},
		func(c *Config) *int { return &c.TUI.SideBySideWidth }),
}

func intSetting(key, desc string, choices []string, field func(*Config) *int) Setting {
	return Setting{
		Key: key, Description: desc, Int: true, Choices: choices,
		get: func(c Config) string { return strconv.Itoa(*field(&c)) },
		set: func(c *Config, v string) { n, _ := strconv.Atoi(v); *field(c) = n },
	}
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

// Value returns the setting's current value in c.
func (s Setting) Value(c Config) string { return s.get(c) }

// Apply returns c with the setting changed to value, after checking it.
func (s Setting) Apply(c Config, value string) (Config, error) {
	value = strings.TrimSpace(value)
	if s.Int {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return c, fmt.Errorf("%s must be a whole number of 0 or more", s.Key)
		}
		value = strconv.Itoa(n)
	} else if value == "" {
		return c, fmt.Errorf("%s must not be empty", s.Key)
	}
	if s.Key == "default_model" {
		if _, err := c.ResolveModel(value); err != nil {
			return c, err
		}
	}
	s.set(&c, value)
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

// SaveSetting writes key = value into the TOML file at path, creating it
// if needed. The rest of the file, comments included, is kept as is.
func SaveSetting(path string, s Setting, value string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var v any = value
	if s.Int {
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Key, err)
		}
		v = n
	}
	out, err := setKey(string(data), s.Key, v)
	if err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
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
	if _, err := tmp.WriteString(out); err != nil {
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

// setKey sets a key ("name" or "table.name") in TOML text. It edits the
// existing line when there is one, or adds the key, and checks that the
// result parses with the new value.
func setKey(text, key string, value any) (string, error) {
	table, name, dotted := strings.Cut(key, ".")
	if !dotted {
		table, name = "", key
	}
	val, err := tomlValue(value)
	if err != nil {
		return "", err
	}
	lines := strings.Split(text, "\n")
	if text == "" {
		lines = nil
	}
	cur := "" // the table the line is in
	start, end := -1, -1
	if table == "" {
		start = 0
	}
	firstHeader := -1
	for i, l := range lines {
		if h, ok := header(l); ok {
			if firstHeader == -1 {
				firstHeader = i
			}
			if cur == table && start != -1 && end == -1 {
				end = i
			}
			cur = h
			if h == table && start == -1 {
				start = i + 1
			}
			continue
		}
		if cur == table && start != -1 && end == -1 {
			if k, rest, ok := keyLine(l); ok && k == name {
				lines[i] = l[:len(l)-len(rest)] + val + trailingComment(rest)
				return checked(strings.Join(lines, "\n"), key, value)
			}
		}
	}
	if start != -1 && end == -1 {
		end = len(lines)
	}
	line := name + " = " + val
	switch {
	case start == -1:
		// No such table yet: add it at the end.
		out := strings.TrimRight(text, "\n")
		if out != "" {
			out += "\n\n"
		}
		return checked(out+"["+table+"]\n"+line+"\n", key, value)
	case table == "" && firstHeader != -1:
		end = firstHeader
	}
	// Insert after the last non-blank line of the section.
	at := end
	for at > start && strings.TrimSpace(lines[at-1]) == "" {
		at--
	}
	if table == "" && at == 0 && firstHeader != -1 {
		// Keep a blank line between the new key and the first table.
		lines = append([]string{line, ""}, lines...)
	} else {
		lines = append(lines[:at], append([]string{line}, lines[at:]...)...)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return checked(out, key, value)
}

// header reports the table name of a "[name]" or "[[name]]" line.
func header(l string) (string, bool) {
	t := strings.TrimSpace(l)
	if !strings.HasPrefix(t, "[") {
		return "", false
	}
	if i := strings.Index(t, "#"); i != -1 {
		t = strings.TrimSpace(t[:i])
	}
	t = strings.TrimSuffix(strings.TrimPrefix(t, "["), "]")
	t = strings.TrimSuffix(strings.TrimPrefix(t, "["), "]")
	return strings.TrimSpace(t), true
}

// keyLine splits "  key = value # note" into the key and the text after "=".
func keyLine(l string) (key, rest string, ok bool) {
	k, rest, ok := strings.Cut(l, "=")
	if !ok {
		return "", "", false
	}
	k = strings.Trim(strings.TrimSpace(k), `"'`)
	if k == "" || strings.HasPrefix(k, "#") {
		return "", "", false
	}
	// Keep the space after "=".
	trimmed := strings.TrimLeft(rest, " \t")
	return k, trimmed, true
}

// trailingComment returns the " # ..." after a simple value, if any.
func trailingComment(rest string) string {
	i := 0
	if strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, "'") {
		q := rest[0]
		j := 1
		for j < len(rest) && rest[j] != q {
			if q == '"' && rest[j] == '\\' {
				j++
			}
			j++
		}
		i = j + 1
	}
	if i > len(rest) {
		return ""
	}
	if c := strings.Index(rest[i:], "#"); c != -1 {
		return " " + rest[i+c:]
	}
	return ""
}

func tomlValue(v any) (string, error) {
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(map[string]any{"v": v}); err != nil {
		return "", err
	}
	_, val, _ := strings.Cut(strings.TrimSpace(b.String()), "= ")
	return val, nil
}

// checked parses text and confirms key now holds value, so an unusual
// layout (an inline table, a dotted key) is refused instead of written
// wrongly.
func checked(text, key string, want any) (string, error) {
	var m map[string]any
	if _, err := toml.Decode(text, &m); err != nil {
		return "", fmt.Errorf("could not set %s: %w", key, err)
	}
	var got any = m
	for _, part := range strings.Split(key, ".") {
		mm, ok := got.(map[string]any)
		if !ok {
			got = nil
			break
		}
		got = mm[part]
	}
	if n, ok := want.(int); ok {
		want = int64(n)
	}
	if got != want {
		return "", fmt.Errorf("could not set %s automatically; edit the file by hand", key)
	}
	return text, nil
}

// GlobalFile is the global config file.
func (p Paths) GlobalFile() string { return filepath.Join(p.Home, "config.toml") }

// ProjectSets reports whether the project config sets key, which then
// overrides the global value.
func (p Paths) ProjectSets(key string) bool {
	if p.ProjectDir == "" || p.ProjectDir == p.Home {
		return false
	}
	var m map[string]any
	md, err := toml.DecodeFile(filepath.Join(p.ProjectDir, "config.toml"), &m)
	return err == nil && md.IsDefined(strings.Split(key, ".")...)
}
