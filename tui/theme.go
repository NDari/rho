package tui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/BurntSushi/toml"
)

// Theme holds the colors used by the TUI. Colors are hex strings ("#rrggbb")
// or ANSI 256 numbers ("39").
type Theme struct {
	Name string `toml:"name"`
	// Base is "dark" or "light"; user themes inherit unset colors from it.
	Base string `toml:"base"`

	Accent      string `toml:"accent"`
	Text        string `toml:"text"`
	Muted       string `toml:"muted"`
	Subtle      string `toml:"subtle"`
	Border      string `toml:"border"`
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
	// Chroma is the syntax highlighting style name.
	Chroma string `toml:"chroma"`
}

// DarkTheme is the default dark theme.
var DarkTheme = Theme{
	Name:        "dark",
	Base:        "dark",
	Accent:      "#7aa2f7",
	Text:        "#c0caf5",
	Muted:       "#7f849c",
	Subtle:      "#45475a",
	Border:      "#3b3f54",
	UserBg:      "#24283b",
	Error:       "#f7768e",
	Warning:     "#e0af68",
	Success:     "#9ece6a",
	ToolName:    "#bb9af7",
	Thinking:    "#6c7086",
	LineNumber:  "#565f89",
	DiffAddBg:   "#1f3326",
	DiffDelBg:   "#3b2027",
	DiffAddEmph: "#2f5a3a",
	DiffDelEmph: "#6b2c37",
	DiffAddSign: "#9ece6a",
	DiffDelSign: "#f7768e",
	StatusBg:    "#1f2335",
	StatusFg:    "#a9b1d6",
	SearchBg:    "#e0af68",
	Chroma:      "catppuccin-mocha",
}

// LightTheme is the default light theme.
var LightTheme = Theme{
	Name:        "light",
	Base:        "light",
	Accent:      "#2e59c7",
	Text:        "#24292f",
	Muted:       "#6e7781",
	Subtle:      "#d0d7de",
	Border:      "#d0d7de",
	UserBg:      "#eef1f6",
	Error:       "#cf222e",
	Warning:     "#9a6700",
	Success:     "#1a7f37",
	ToolName:    "#8250df",
	Thinking:    "#8c959f",
	LineNumber:  "#8c959f",
	DiffAddBg:   "#e6ffec",
	DiffDelBg:   "#ffebe9",
	DiffAddEmph: "#abf2bc",
	DiffDelEmph: "#ffc1bc",
	DiffAddSign: "#1a7f37",
	DiffDelSign: "#cf222e",
	StatusBg:    "#eaeef2",
	StatusFg:    "#424a53",
	SearchBg:    "#fff8c5",
	Chroma:      "catppuccin-latte",
}

// IsDark reports whether the theme has a dark background.
func (t Theme) IsDark() bool { return t.Base != "light" }

// c converts a theme color string to a color.
func c(s string) color.Color {
	if s == "" {
		return nil
	}
	return lipgloss.Color(s)
}

// LoadThemes returns the built-in themes plus user themes from
// ~/.rho/themes/*.toml, keyed by name.
func LoadThemes(home string) (map[string]Theme, []string) {
	themes := map[string]Theme{"dark": DarkTheme, "light": LightTheme}
	var warnings []string
	files, _ := filepath.Glob(filepath.Join(home, "themes", "*.toml"))
	for _, f := range files {
		var t Theme
		if _, err := toml.DecodeFile(f, &t); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		if t.Name == "" {
			t.Name = strings.TrimSuffix(filepath.Base(f), ".toml")
		}
		base := DarkTheme
		if t.Base == "light" {
			base = LightTheme
		}
		themes[t.Name] = mergeTheme(base, t)
	}
	return themes, warnings
}

// ThemeNames returns sorted theme names.
func ThemeNames(themes map[string]Theme) []string {
	names := make([]string, 0, len(themes))
	for n := range themes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func mergeTheme(base, over Theme) Theme {
	out := base
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	out.Name = over.Name
	set(&out.Base, over.Base)
	set(&out.Accent, over.Accent)
	set(&out.Text, over.Text)
	set(&out.Muted, over.Muted)
	set(&out.Subtle, over.Subtle)
	set(&out.Border, over.Border)
	set(&out.UserBg, over.UserBg)
	set(&out.Error, over.Error)
	set(&out.Warning, over.Warning)
	set(&out.Success, over.Success)
	set(&out.ToolName, over.ToolName)
	set(&out.Thinking, over.Thinking)
	set(&out.LineNumber, over.LineNumber)
	set(&out.DiffAddBg, over.DiffAddBg)
	set(&out.DiffDelBg, over.DiffDelBg)
	set(&out.DiffAddEmph, over.DiffAddEmph)
	set(&out.DiffDelEmph, over.DiffDelEmph)
	set(&out.DiffAddSign, over.DiffAddSign)
	set(&out.DiffDelSign, over.DiffDelSign)
	set(&out.StatusBg, over.StatusBg)
	set(&out.StatusFg, over.StatusFg)
	set(&out.SearchBg, over.SearchBg)
	set(&out.Chroma, over.Chroma)
	return out
}

// styles are lipgloss styles derived from a theme.
type styles struct {
	text, muted, subtle, accent, errorS, warning, success lipgloss.Style
	toolName, thinking, lineNum, border                   lipgloss.Style
	user, status, statusKey, title, selected, search      lipgloss.Style
}

func newStyles(t Theme) styles {
	s := lipgloss.NewStyle
	return styles{
		text:      s().Foreground(c(t.Text)),
		muted:     s().Foreground(c(t.Muted)),
		subtle:    s().Foreground(c(t.Subtle)),
		accent:    s().Foreground(c(t.Accent)),
		errorS:    s().Foreground(c(t.Error)),
		warning:   s().Foreground(c(t.Warning)),
		success:   s().Foreground(c(t.Success)),
		toolName:  s().Foreground(c(t.ToolName)).Bold(true),
		thinking:  s().Foreground(c(t.Thinking)).Italic(true),
		lineNum:   s().Foreground(c(t.LineNumber)),
		border:    s().Foreground(c(t.Border)),
		user:      s().Background(c(t.UserBg)).Foreground(c(t.Text)),
		status:    s().Background(c(t.StatusBg)).Foreground(c(t.StatusFg)),
		statusKey: s().Background(c(t.StatusBg)).Foreground(c(t.Accent)).Bold(true),
		title:     s().Foreground(c(t.Accent)).Bold(true),
		selected:  s().Background(c(t.UserBg)).Foreground(c(t.Accent)).Bold(true),
		search:    s().Background(c(t.SearchBg)).Foreground(lipgloss.Color("#000000")),
	}
}

// pickTheme picks the configured theme, falling back to dark.
func pickTheme(themes map[string]Theme, name string) (Theme, string) {
	if t, ok := themes[name]; ok {
		return t, ""
	}
	if name != "" {
		return DarkTheme, fmt.Sprintf("unknown theme %q; using dark", name)
	}
	return DarkTheme, ""
}
