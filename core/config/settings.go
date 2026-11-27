package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/NDari/rho/core/jsonc"
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
	// entry in its provider's models.
	PerModel bool
	// Optional settings accept "" (shown as "default"), which removes the
	// key so the built-in behavior applies.
	Optional bool
	Choices  []string
	get      func(c Config, model string) string
	set      func(c *Config, model, v string)
}

// Settings lists the changeable settings. default_model and the theme
// settings take their choices from the configured models and themes, so
// they have none here.
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
	{Key: "theme", Description: "color theme (system: theme_dark or theme_light, to match the system)",
		get: func(c Config, _ string) string { return c.Theme },
		set: func(c *Config, _, v string) { c.Theme = v }},
	{Key: "theme_dark", Description: "theme for theme = system when the system is dark",
		get: func(c Config, _ string) string { return c.ThemeDark },
		set: func(c *Config, _, v string) { c.ThemeDark = v }},
	{Key: "theme_light", Description: "theme for theme = system when the system is light",
		get: func(c Config, _ string) string { return c.ThemeLight },
		set: func(c *Config, _, v string) { c.ThemeLight = v }},
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

// modelEntry returns the models entry for id, adding one for an ad-hoc
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
// because no provider in the file lists the model.
var ErrModelNotInFile = errors.New("no provider in the file lists the model")

// SaveSetting writes the setting's value into the config file at path,
// creating the file if needed. A per-model setting goes into the entry
// with id model in its provider's models. An empty optional value removes the key. Comments
// in the file are kept, and the file is formatted.
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
	key := strings.Split(s.Key, ".")
	if s.PerModel {
		var file Config
		if _, err := jsonc.Decode(data, &file); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		prov, i := file.findModel(model)
		if i < 0 {
			return ErrModelNotInFile
		}
		key = append([]string{"providers", prov, "models", strconv.Itoa(i)}, key...)
	}
	out, err := jsonc.Set(data, key, v)
	if err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	return writeFile(path, out)
}

// findModel returns the provider that lists the model with id in a decoded
// file, and the model's index in its models, or -1.
func (c Config) findModel(id string) (string, int) {
	for name, p := range c.Providers {
		if i := slices.IndexFunc(p.Models, func(m Model) bool { return m.ID == id }); i >= 0 {
			return name, i
		}
	}
	return "", -1
}

// writeFile replaces path atomically, keeping its permissions.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.jsonc")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
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

// SetExtensionDisabled adds name to (or removes it from)
// extensions.disabled in the config file at path.
func SetExtensionDisabled(path, name string, disabled bool) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var file Config
	if _, err := jsonc.Decode(data, &file); err != nil {
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
	out, err := jsonc.Set(data, []string{"extensions", "disabled"}, list)
	if err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	return writeFile(path, out)
}

// GlobalFile is the global config file.
func (p Paths) GlobalFile() string { return filepath.Join(p.Home, FileName) }

// ProjectFile is the project config file, or "" when project files are
// ignored or there is no project directory apart from Home.
func (p Paths) ProjectFile() string { return p.Project(FileName) }

// ConvertLegacy writes the config file at path from the TOML file
// (config.toml) next to it, when only that one exists. It returns a
// warning saying so, or describing why it failed.
func ConvertLegacy(path string) string {
	legacy := filepath.Join(filepath.Dir(path), legacyFileName)
	ok, err := jsonc.ConvertTOML(legacy, path, nestModels)
	switch {
	case err != nil:
		return fmt.Sprintf("%s is no longer read, and converting it to %s failed: %v", legacy, FileName, err)
	case ok:
		return fmt.Sprintf("converted %s to %s, which Rho now reads instead; comments were not carried over", legacy, path)
	}
	return ""
}

// readProject decodes the project config file, if there is one.
func (p Paths) readProject() (Config, jsonc.Meta, bool) {
	path := p.ProjectFile()
	if path == "" {
		return Config{}, jsonc.Meta{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, jsonc.Meta{}, false
	}
	var c Config
	md, err := jsonc.Decode(data, &c)
	if err != nil {
		return Config{}, jsonc.Meta{}, false
	}
	return c, md, true
}

// ProjectSets reports whether the project config sets key (for a
// per-model setting, defines the model at all), which then overrides the
// global value.
func (p Paths) ProjectSets(s Setting, model string) bool {
	c, md, ok := p.readProject()
	if !ok {
		return false
	}
	if s.PerModel {
		_, i := c.findModel(model)
		return i >= 0
	}
	return md.IsDefined(strings.Split(s.Key, ".")...)
}

// ProjectDisables reports whether the project config lists extension name
// in extensions.disabled.
func (p Paths) ProjectDisables(name string) bool {
	c, _, ok := p.readProject()
	return ok && slices.Contains(c.Extensions.Disabled, name)
}
