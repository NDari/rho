package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	chromastyles "github.com/alecthomas/chroma/v2/styles"
)

func TestBuiltins(t *testing.T) {
	s := Builtin()
	if n := len(s.Names()); n != 17 {
		t.Fatalf("got %d built-in themes, want 17: %v", n, s.Names())
	}
	light := map[string]bool{
		"gruvbox-light": true, "github-light": true, "catppuccin-latte": true, "tokyo-night-day": true,
		"rose-pine-dawn": true, "solarized-light": true, "kanagawa-lotus": true,
	}
	for _, name := range s.Names() {
		th, _ := s.Get(name)
		if _, ok := chromastyles.Registry[th.Chroma]; !ok {
			t.Errorf("%s: unknown chroma style %q", name, th.Chroma)
		}
		if th.IsDark() == light[name] {
			t.Errorf("%s: IsDark() = %v", name, th.IsDark())
		}
		// Every role is set after deriving, so no frontend sees an empty color.
		for k, v := range th.CSSVars() {
			if v == "" {
				t.Errorf("%s: --%s is empty", name, k)
			}
		}
		if th.SearchFg == "" || th.DiffAddEmph == "" || th.StatusFg == "" {
			t.Errorf("%s: derived roles missing: %+v", name, th)
		}
	}
}

func TestResolve(t *testing.T) {
	s := Builtin()
	tests := []struct {
		name, theme, dark, light string
		systemDark               bool
		want                     string
		warn                     bool
	}{
		{"named", "nord", "", "", false, "nord", false},
		{"old name dark", "dark", "", "", true, "gruvbox-dark", true},
		{"old name light", "light", "", "", false, "github-light", true},
		{"system dark default", "system", "", "", true, "gruvbox-dark", false},
		{"system light default", "", "", "", false, "github-light", false},
		{"system dark chosen", "system", "dracula", "solarized-light", true, "dracula", false},
		{"system light chosen", "system", "dracula", "solarized-light", false, "solarized-light", false},
		{"unknown", "nope", "", "", true, "gruvbox-dark", true},
		{"unknown system pick", "system", "nope", "", true, "gruvbox-dark", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warn := s.Resolve(tt.theme, tt.dark, tt.light, tt.systemDark)
			if got.Name != tt.want || (warn != "") != tt.warn {
				t.Errorf("got %q, warning %q; want %q, warning %v", got.Name, warn, tt.want, tt.warn)
			}
		})
	}
}

func TestUserThemes(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "themes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"mine.toml":    "base = \"nord\"\naccent = \"#ff0000\"\n",
		"old.toml":     "base = \"github-light\"\n",
		"bad.toml":     "base = \"nope\"\nchroma = \"nope\"\ncolour = \"x\"\n",
		"broken.toml":  "accent = \n",
		"plain.toml":   "accent = \"#00ff00\"\n",
		"bgonly.toml":  "base = \"nord\"\nbackground = \"#000000\"\n",
		"ansi256.toml": "accent = \"39\"\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, warnings := Load(home)
	nord, _ := s.Get("nord")

	mine, ok := s.Get("mine")
	if !ok || mine.Accent != "#ff0000" || mine.Background != nord.Background || mine.Chroma != "nord" {
		t.Errorf("mine = %+v", mine)
	}
	// The accent stands in for the tool name only where the base left it
	// to be derived; nord sets its own.
	if mine.ToolName != nord.ToolName {
		t.Errorf("mine.ToolName = %q, want nord's %q", mine.ToolName, nord.ToolName)
	}
	if old, _ := s.Get("old"); old.Background != "#ffffff" {
		t.Errorf("base = \"github-light\" should start from it, got background %q", old.Background)
	}
	if plain, _ := s.Get("plain"); plain.Chroma != "gruvbox" {
		t.Errorf("a theme without base should start from gruvbox-dark, got chroma %q", plain.Chroma)
	}
	// A new background re-derives the roles nord leaves empty.
	if bg, _ := s.Get("bgonly"); bg.Muted == nord.Muted {
		t.Errorf("bgonly.Muted = %q, want it derived from the new background", bg.Muted)
	}
	if a, _ := s.Get("ansi256"); a.Accent != "39" || a.ToolName != "#d3869b" {
		t.Errorf("ansi256 = %q / %q", a.Accent, a.ToolName)
	}

	joined := strings.Join(warnings, "\n")
	for _, want := range []string{`unknown base theme "nope"`, `unknown chroma style "nope"`, `unknown key "colour"`, "broken.toml"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q lack %q", joined, want)
		}
	}
}

func TestMixAndContrast(t *testing.T) {
	if got := Mix("#000000", "#ffffff", 0.5); got != "#808080" {
		t.Errorf("Mix = %q", got)
	}
	if got := Mix("39", "#ffffff", 0.5); got != "" {
		t.Errorf("Mix with an ANSI color = %q, want empty", got)
	}
	if Contrast("#fabd2f") != "#000000" || Contrast("#1f2328") != "#ffffff" {
		t.Error("Contrast picked the wrong text color")
	}
}

func TestCSSRule(t *testing.T) {
	th, _ := Builtin().Get("github-light")
	r := th.CSSRule(":root")
	for _, want := range []string{":root { color-scheme: light;", "--bg: #ffffff;", "--hl-keyword: #cf222e;"} {
		if !strings.Contains(r, want) {
			t.Errorf("rule lacks %q: %s", want, r)
		}
	}
}
