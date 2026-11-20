package config

import (
	"os"
	"path/filepath"
	"runtime"
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
	if err := SaveSetting(path, s, "300"); err != nil {
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
	if _, err := s.Apply(c, "-1"); err == nil {
		t.Error("negative: expected an error")
	}
	got, err := s.Apply(c, " 7 ")
	if err != nil || got.MaxRetries != 7 || c.MaxRetries == 7 {
		t.Errorf("apply: %v, %d", err, got.MaxRetries)
	}
	d, _ := LookupSetting("default_model")
	if _, err := d.Apply(c, "nope"); err == nil {
		t.Error("unknown model: expected an error")
	}
	if got, err := d.Apply(c, "m"); err != nil || got.DefaultModel != "m" {
		t.Errorf("default_model: %v", err)
	}
}
