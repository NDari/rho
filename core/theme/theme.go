// Package theme holds Rho's color themes: the built-in set, user themes
// from ~/.rho/themes/, and the colors each frontend derives from them. It
// is data only; the TUI, the GUI and the HTML export turn it into styles.
package theme

import (
	"embed"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
)

// System is the theme setting that follows the system's light or dark
// mode, using the theme_light and theme_dark settings.
const System = "system"

// Default themes for the system's dark and light modes.
const (
	DefaultDark  = "gruvbox-dark"
	DefaultLight = "github-light"
)

// Theme is a set of color roles. Colors are hex strings ("#rrggbb") or
// ANSI 256 numbers ("39"). Roles left empty are derived from the others
// (see derive), so a theme only needs its palette's main colors.
type Theme struct {
	Name string `toml:"name"`
	// Base names the theme this one starts from (user themes only).
	Base string `toml:"base"`

	Background  string `toml:"background"`
	Surface     string `toml:"surface"`
	Text        string `toml:"text"`
	Muted       string `toml:"muted"`
	Subtle      string `toml:"subtle"`
	Border      string `toml:"border"`
	Accent      string `toml:"accent"`
	UserBg      string `toml:"user_bg"`
	Error       string `toml:"error"`
	Warning     string `toml:"warning"`
	Success     string `toml:"success"`
	ToolName    string `toml:"tool_name"`
	Thinking    string `toml:"thinking"`
	LineNumber  string `toml:"line_number"`
	DiffAddBg   string `toml:"diff_add_bg"`
	DiffDelBg   string `toml:"diff_del_bg"`
	DiffAddEmph string `toml:"diff_add_emph"`
	DiffDelEmph string `toml:"diff_del_emph"`
	DiffAddSign string `toml:"diff_add_sign"`
	DiffDelSign string `toml:"diff_del_sign"`
	StatusBg    string `toml:"status_bg"`
	StatusFg    string `toml:"status_fg"`
	SearchBg    string `toml:"search_bg"`
	SearchFg    string `toml:"search_fg"`
	// Chroma is the syntax highlighting style (one of Chroma's styles).
	Chroma string `toml:"chroma"`
}

// IsDark reports whether the theme has a dark background.
func (t Theme) IsDark() bool {
	r, g, b, ok := parseHex(t.Background)
	if !ok {
		return true
	}
	return luminance(r, g, b) < 0.4
}

// ChromaStyle returns the theme's syntax highlighting style.
func (t Theme) ChromaStyle() *chroma.Style {
	if st, ok := chromastyles.Registry[t.Chroma]; ok {
		return st
	}
	return chromastyles.Fallback
}

//go:embed themes/*.toml
var builtinFS embed.FS

// Set is the built-in and user themes, keyed by name.
type Set struct {
	themes map[string]Theme
}

// Builtin returns the built-in themes only.
func Builtin() Set {
	s := Set{themes: map[string]Theme{}}
	for name, t := range written {
		s.themes[name] = derive(t)
	}
	return s
}

// Load returns the built-in themes plus the user themes in home/themes/,
// with warnings for files that could not be read or name an unknown base
// or Chroma style.
func Load(home string) (Set, []string) {
	s := Builtin()
	var warnings []string
	files, _ := filepath.Glob(filepath.Join(home, "themes", "*.toml"))
	for _, f := range files {
		var t Theme
		md, err := toml.DecodeFile(f, &t)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		for _, k := range md.Undecoded() {
			warnings = append(warnings, fmt.Sprintf("%s: unknown key %q", f, k.String()))
		}
		if t.Name == "" {
			t.Name = strings.TrimSuffix(filepath.Base(f), ".toml")
		}
		baseName := t.Base
		if baseName == "" {
			baseName = DefaultDark
		}
		base, ok := s.Get(baseName)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s: unknown base theme %q; using %s", f, baseName, DefaultDark))
			base, _ = s.Get(DefaultDark)
		}
		merged := merge(base.raw(), t)
		if merged.Chroma != "" {
			if _, ok := chromastyles.Registry[merged.Chroma]; !ok {
				warnings = append(warnings, fmt.Sprintf("%s: unknown chroma style %q", f, merged.Chroma))
			}
		}
		s.themes[t.Name] = derive(merged)
	}
	return s, warnings
}

// Get returns a theme by name.
func (s Set) Get(name string) (Theme, bool) {
	t, ok := s.themes[name]
	return t, ok
}

// Names returns the theme names, sorted.
func (s Set) Names() []string {
	names := make([]string, 0, len(s.themes))
	for n := range s.themes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Valid reports whether name is a theme or "system".
func (s Set) Valid(name string) bool {
	if name == System {
		return true
	}
	_, ok := s.Get(name)
	return ok
}

// Resolve picks the theme for the settings: name is the theme setting,
// dark and light the themes for "system", and systemDark whether the
// system is in dark mode. Unknown names fall back to the defaults with a
// warning.
func (s Set) Resolve(name, dark, light string, systemDark bool) (Theme, string) {
	if name == "" {
		name = System
	}
	if name == System {
		pick, def := light, DefaultLight
		if systemDark {
			pick, def = dark, DefaultDark
		}
		if pick == "" {
			pick = def
		}
		if t, ok := s.Get(pick); ok {
			return t, ""
		}
		t, _ := s.Get(def)
		return t, fmt.Sprintf("unknown theme %q; using %s", pick, def)
	}
	if t, ok := s.Get(name); ok {
		return t, ""
	}
	def := DefaultLight
	if systemDark {
		def = DefaultDark
	}
	t, _ := s.Get(def)
	return t, fmt.Sprintf("unknown theme %q; using %s", name, def)
}

// raw is the theme as written, before derived roles were filled in, so a
// user theme based on it derives its own. Built-ins keep their written
// values in written.
func (t Theme) raw() Theme {
	if w, ok := written[t.Name]; ok {
		return w
	}
	return t
}

// written keeps each built-in as written in its file.
var written = func() map[string]Theme {
	m := map[string]Theme{}
	files, _ := fs.Glob(builtinFS, "themes/*.toml")
	for _, f := range files {
		data, err := builtinFS.ReadFile(f)
		if err != nil {
			panic(err) // embedded; cannot fail
		}
		var t Theme
		if _, err := toml.Decode(string(data), &t); err != nil {
			panic(fmt.Sprintf("built-in theme %s: %v", f, err))
		}
		t.Name = strings.TrimSuffix(filepath.Base(f), ".toml")
		m[t.Name] = t
	}
	return m
}()

// merge returns base with every non-empty string field of over set on it.
func merge(base, over Theme) Theme {
	out := base
	bv, ov := reflect.ValueOf(&out).Elem(), reflect.ValueOf(over)
	for i := range ov.NumField() {
		if v := ov.Field(i).String(); v != "" {
			bv.Field(i).SetString(v)
		}
	}
	out.Name = over.Name
	return out
}

// derive fills in the roles a theme left empty from its main colors:
// surfaces and borders are the text mixed into the background, and the
// diff backgrounds are the success and error colors mixed into it.
func derive(t Theme) Theme {
	or := func(dst *string, v string) {
		if *dst == "" {
			*dst = v
		}
	}
	bg, fg := t.Background, t.Text
	or(&t.Surface, Mix(bg, fg, 0.05))
	or(&t.UserBg, Mix(bg, fg, 0.10))
	or(&t.Border, Mix(bg, fg, 0.20))
	or(&t.Subtle, Mix(bg, fg, 0.35))
	or(&t.Muted, Mix(bg, fg, 0.60))
	or(&t.Thinking, t.Muted)
	or(&t.LineNumber, t.Subtle)
	or(&t.ToolName, t.Accent)
	or(&t.StatusBg, t.UserBg)
	or(&t.StatusFg, t.Muted)
	or(&t.DiffAddSign, t.Success)
	or(&t.DiffDelSign, t.Error)
	add, del := 0.14, 0.14
	if !t.IsDark() {
		add, del = 0.16, 0.14
	}
	or(&t.DiffAddBg, Mix(bg, t.DiffAddSign, add))
	or(&t.DiffDelBg, Mix(bg, t.DiffDelSign, del))
	or(&t.DiffAddEmph, Mix(bg, t.DiffAddSign, 2*add))
	or(&t.DiffDelEmph, Mix(bg, t.DiffDelSign, 2*del))
	or(&t.SearchBg, t.Warning)
	or(&t.SearchFg, Contrast(t.SearchBg))
	return t
}

// Mix returns a mixed with b by amount (0: a, 1: b). It returns "" when
// either color is not a hex color.
func Mix(a, b string, amount float64) string {
	ar, ag, ab, ok1 := parseHex(a)
	br, bg, bb, ok2 := parseHex(b)
	if !ok1 || !ok2 {
		return ""
	}
	m := func(x, y float64) int { return int(math.Round(x + (y-x)*amount)) }
	return fmt.Sprintf("#%02x%02x%02x", m(ar, br), m(ag, bg), m(ab, bb))
}

// Contrast returns black or white, whichever has the higher WCAG contrast
// ratio on bg. The two are equal at a luminance of about 0.18.
func Contrast(bg string) string {
	r, g, b, ok := parseHex(bg)
	if !ok || luminance(r, g, b) > 0.179 {
		return "#000000"
	}
	return "#ffffff"
}

func parseHex(s string) (r, g, b float64, ok bool) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return 0, 0, 0, false
	}
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return float64(n >> 16 & 0xff), float64(n >> 8 & 0xff), float64(n & 0xff), true
}

// luminance is the relative luminance (WCAG) of an sRGB color.
func luminance(r, g, b float64) float64 {
	lin := func(c float64) float64 {
		c /= 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}
