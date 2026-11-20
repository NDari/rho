package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSetKey(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		key   string
		value any
		want  string
	}{
		{"empty file", "", "theme", "light", "theme = \"light\"\n"},
		{"empty file table", "", "bash.timeout", 60, "[bash]\ntimeout = 60\n"},
		{
			"replace top level keeps comments",
			"# top\ntheme = \"dark\" # mine\n\n[bash]\ntimeout = 5\n",
			"theme", "light",
			"# top\ntheme = \"light\" # mine\n\n[bash]\ntimeout = 5\n",
		},
		{
			"replace in table",
			"theme = \"dark\"\n\n[bash]\n  timeout=5\nshell = \"sh\"\n",
			"bash.timeout", 60,
			"theme = \"dark\"\n\n[bash]\n  timeout=60\nshell = \"sh\"\n",
		},
		{
			"same key in another table is left alone",
			"[extensions]\ntimeout = 30\n\n[bash]\ntimeout = 5\n",
			"bash.timeout", 60,
			"[extensions]\ntimeout = 30\n\n[bash]\ntimeout = 60\n",
		},
		{
			"add top level before first table",
			"# comment\n\n[providers.local]\nbase_url = \"http://localhost:8080/v1\"\n",
			"max_retries", 5,
			"# comment\nmax_retries = 5\n\n[providers.local]\nbase_url = \"http://localhost:8080/v1\"\n",
		},
		{
			"add top level when file starts with a table",
			"[bash]\ntimeout = 5\n",
			"theme", "light",
			"theme = \"light\"\n\n[bash]\ntimeout = 5\n",
		},
		{
			"add to existing table",
			"[bash]\ntimeout = 5\n\n[tui]\nside_by_side_width = 120\n",
			"bash.max_timeout", 600,
			"[bash]\ntimeout = 5\nmax_timeout = 600\n\n[tui]\nside_by_side_width = 120\n",
		},
		{
			"add missing table at end",
			"theme = \"dark\"\n",
			"compaction.reserve", 32000,
			"theme = \"dark\"\n\n[compaction]\nreserve = 32000\n",
		},
		{
			"commented out key is not edited",
			"# theme = \"dark\"\n",
			"theme", "light",
			"# theme = \"dark\"\ntheme = \"light\"\n",
		},
		{
			"sub-table is not the table",
			"[tui.keybindings]\nsubmit = \"enter\"\n",
			"tui.side_by_side_width", 0,
			"[tui.keybindings]\nsubmit = \"enter\"\n\n[tui]\nside_by_side_width = 0\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := setKey(tt.in, tt.key, tt.value)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestSetKeyRefusesUnusualLayout(t *testing.T) {
	if _, err := setKey("bash = { timeout = 5 }\n", "bash.timeout", 60); err == nil {
		t.Fatal("inline table: expected an error")
	}
}

func TestNext(t *testing.T) {
	choices := []string{"30", "60", "120"}
	for _, tt := range []struct{ cur, want string }{
		{"30", "60"}, {"120", "30"}, {"45", "60"}, {"500", "30"}, {"x", "30"},
	} {
		if got := Next(choices, tt.cur, true); got != tt.want {
			t.Errorf("Next(%q) = %q, want %q", tt.cur, got, tt.want)
		}
	}
}

func TestSaveSettingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("# keep me\n[bash]\ntimeout = 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := LookupSetting("bash.timeout")
	if err := SaveSetting(path, s, "", "300"); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(Paths{Home: dir})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bash.Timeout != 300 {
		t.Errorf("timeout = %d", cfg.Bash.Timeout)
	}
	// Windows has no Unix permission bits to keep.
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if string(data) != "# keep me\n[bash]\ntimeout = 300\n" {
		t.Errorf("file = %q", data)
	}
}

func TestApplySetting(t *testing.T) {
	c := Defaults()
	c.Models = []Model{{ID: "m", Provider: "openai"}}
	s, _ := LookupSetting("max_retries")
	if _, err := s.Apply(c, "", "-1"); err == nil {
		t.Error("negative: expected an error")
	}
	got, err := s.Apply(c, "", " 7 ")
	if err != nil || got.MaxRetries != 7 || c.MaxRetries == 7 {
		t.Errorf("apply: %v, %d", err, got.MaxRetries)
	}
	d, _ := LookupSetting("default_model")
	if _, err := d.Apply(c, "", "nope"); err == nil {
		t.Error("unknown model: expected an error")
	}
	if got, err := d.Apply(c, "", "m"); err != nil || got.DefaultModel != "m" {
		t.Errorf("default_model: %v", err)
	}
}

func TestSetKeyBoolAndRemove(t *testing.T) {
	got, err := setKey("[compaction]\nreserve = 1\n", "compaction.auto", false)
	if err != nil || got != "[compaction]\nreserve = 1\nauto = false\n" {
		t.Errorf("add bool: %q, %v", got, err)
	}
	got, err = setKey("a = 1\ntheme = \"x\"\nb = 2\n", "theme", nil)
	if err != nil || got != "a = 1\nb = 2\n" {
		t.Errorf("remove: %q, %v", got, err)
	}
	if got, err := setKey("a = 1\n", "theme", nil); err != nil || got != "a = 1\n" {
		t.Errorf("remove missing: %q, %v", got, err)
	}
}

func TestSetModelKey(t *testing.T) {
	in := `default_model = "b"

[[models]]
id = "a"
provider = "p"

# the second one
[[models]]
id = "b"   # mine
provider = "p"
reasoning_effort = "low"

[bash]
timeout = 5
`
	got, err := setModelKey(in, "b", "reasoning_effort", "high")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(in, `reasoning_effort = "low"`, `reasoning_effort = "high"`, 1); got != want {
		t.Errorf("replace:\n%s", got)
	}
	got, err = setModelKey(in, "a", "reasoning_effort", "medium")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(in, "provider = \"p\"\n\n#", "provider = \"p\"\nreasoning_effort = \"medium\"\n\n#", 1); got != want {
		t.Errorf("add:\n%s", got)
	}
	got, err = setModelKey(in, "b", "reasoning_effort", nil)
	if err != nil || strings.Contains(got, "reasoning_effort") {
		t.Errorf("remove: %v\n%s", err, got)
	}
	if _, err := setModelKey(in, "c", "reasoning_effort", "high"); !errors.Is(err, ErrModelNotInFile) {
		t.Errorf("missing model: %v", err)
	}
}

func TestApplyPerModelAndBool(t *testing.T) {
	c := Defaults()
	c.Models = []Model{{ID: "m", Provider: "openai"}}
	r, _ := LookupSetting("reasoning_effort")
	got, err := r.Apply(c, "m", "high")
	if err != nil || got.Models[0].ReasoningEffort != "high" || c.Models[0].ReasoningEffort != "" {
		t.Fatalf("apply: %v %+v", err, got.Models)
	}
	if v := r.Value(got, "m"); v != "high" {
		t.Errorf("value = %q", v)
	}
	if got, _ := r.Apply(got, "m", "default"); got.Models[0].ReasoningEffort != "" {
		t.Error("default did not clear")
	}
	adhoc, err := r.Apply(c, "openai/x", "low")
	if err != nil || r.Value(adhoc, "openai/x") != "low" {
		t.Errorf("ad-hoc model: %v", err)
	}
	if m, _ := adhoc.ResolveModel("openai/x"); m.APIName() != "x" {
		t.Errorf("ad-hoc api name = %q", m.APIName())
	}

	a, _ := LookupSetting("compaction.auto")
	if a.Value(c, "") != "true" {
		t.Error("auto should default to true")
	}
	off, err := a.Apply(c, "", "off")
	if err != nil || off.Compaction.AutoEnabled() {
		t.Errorf("off: %v", err)
	}
	if _, err := a.Apply(c, "", "maybe"); err == nil {
		t.Error("expected an error for a non-bool")
	}
}

func TestSetExtensionDisabled(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		ext      string
		disabled bool
		want     string
	}{
		{"new file", "", "a", true, "[extensions]\ndisabled = [\"a\"]\n"},
		{"add to list", "[extensions]\ntimeout = 30\ndisabled = [\"a\"] # off\n", "b", true,
			"[extensions]\ntimeout = 30\ndisabled = [\"a\", \"b\"] # off\n"},
		{"already there", "[extensions]\ndisabled = [\"a\"]\n", "a", true, "[extensions]\ndisabled = [\"a\"]\n"},
		{"remove last", "[extensions]\ndisabled = [\"a\"]\n", "a", false, "[extensions]\ndisabled = []\n"},
		{"multi-line list", "[extensions]\ndisabled = [\n  \"a\", # first\n  \"b]\",\n]\ntimeout = 30\n", "a", false,
			"[extensions]\ndisabled = [\"b]\"]\ntimeout = 30\n"},
		{"remove missing", "theme = \"dark\"\n", "a", false, "theme = \"dark\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if tt.in != "" {
				if err := os.WriteFile(path, []byte(tt.in), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := SetExtensionDisabled(path, tt.ext, tt.disabled); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(path); string(got) != tt.want {
				t.Errorf("got\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}
