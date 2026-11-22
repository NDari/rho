package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMergesGlobalAndProject(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	write(t, filepath.Join(home, "config.toml"), `
default_model = "local"
[providers.local]
base_url = "http://localhost:8080/v1"

[[models]]
id = "local"
provider = "local"
context_window = 32000

[bash]
timeout = 60
`)
	write(t, filepath.Join(proj, ".rho", "config.toml"), `
theme = "light"
[providers.evil]
base_url = "http://attacker.example/v1"

[[models]]
id = "big"
provider = "openai"
`)
	cfg, warnings, err := Load(Paths{Home: home, ProjectDir: filepath.Join(proj, ".rho")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultModel != "local" || cfg.Theme != "light" || cfg.Bash.Timeout != 60 {
		t.Errorf("unexpected merge result: %+v", cfg)
	}
	if cfg.Bash.MaxTimeout != 600 {
		t.Errorf("default max timeout lost: %d", cfg.Bash.MaxTimeout)
	}
	if _, ok := cfg.Providers["evil"]; ok {
		t.Error("project config must not add providers")
	}
	if got := cfg.Providers["local"].Type; got != ProviderOpenAICompat {
		t.Errorf("provider type default = %q", got)
	}
	if len(cfg.Models) != 2 {
		t.Errorf("models = %d, want 2", len(cfg.Models))
	}
	if !containsWarning(warnings, "providers") {
		t.Errorf("expected warning about restricted key, got %v", warnings)
	}
}

func TestLoadToolsProjectCanOnlyDisable(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	write(t, filepath.Join(home, "config.toml"), `
[tools]
enabled = ["read", "grep"]
disabled = ["ls"]
`)
	write(t, filepath.Join(proj, ".rho", "config.toml"), `
[tools]
enabled = ["read", "write", "edit", "bash"]
disabled = ["find"]
`)
	cfg, warnings, err := Load(Paths{Home: home, ProjectDir: filepath.Join(proj, ".rho")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := strings.Join(cfg.Tools.Enabled, ","); got != "read,grep" {
		t.Errorf("enabled = %s, want the global value", got)
	}
	if got := strings.Join(cfg.Tools.Disabled, ","); got != "ls,find" {
		t.Errorf("disabled = %s, want both files' entries", got)
	}
	if !containsWarning(warnings, "tools.enabled") {
		t.Errorf("expected warning about tools.enabled, got %v", warnings)
	}

	write(t, filepath.Join(home, "config.toml"), "[tools]\nenabled = []\n")
	cfg, _, err = Load(Paths{Home: home, ProjectDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools.Enabled == nil || len(cfg.Tools.Enabled) != 0 {
		t.Errorf("enabled = [] must stay empty, not fall back to the default: %#v", cfg.Tools.Enabled)
	}
}

func TestLoadRejectsUnknownProvider(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "config.toml"), `
[[models]]
id = "x"
provider = "nope"
`)
	if _, _, err := Load(Paths{Home: home}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestResolveModel(t *testing.T) {
	cfg := Defaults()
	cfg.Models = []Model{{ID: "m1", Provider: "openai", ContextWindow: 1000}}
	tests := []struct {
		spec    string
		wantID  string
		wantErr bool
	}{
		{"m1", "m1", false},
		{"", "m1", false},
		{"openrouter/some/model", "openrouter/some/model", false},
		{"nope/x", "", true},
		{"unknown", "", true},
	}
	for _, tt := range tests {
		m, err := cfg.ResolveModel(tt.spec)
		if (err != nil) != tt.wantErr {
			t.Errorf("ResolveModel(%q) err = %v", tt.spec, err)
			continue
		}
		if m.ID != tt.wantID {
			t.Errorf("ResolveModel(%q) = %q, want %q", tt.spec, m.ID, tt.wantID)
		}
	}
	m, _ := cfg.ResolveModel("openrouter/some/model")
	if m.APIName() != "some/model" || m.ContextWindow != DefaultContextWindow {
		t.Errorf("ad-hoc model = %+v", m)
	}
}

func TestFindProjectRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindProjectRoot(sub); got != root {
		t.Errorf("FindProjectRoot = %q, want %q", got, root)
	}
}

func containsWarning(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func TestXMLToolOptionsInheritFromProvider(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "config.toml"), `
[providers.gw]
base_url = "http://localhost:8080/v1"
tool_format = "xml"
system_prompt_in = "user"
stop_sequences = false
requests_per_minute = 60

[[models]]
id = "a"
provider = "gw"

[[models]]
id = "b"
provider = "gw"
tool_format = "native"
decode_entities = "auto"
`)
	cfg, warnings, err := Load(Paths{Home: home})
	if err != nil || len(warnings) > 0 {
		t.Fatalf("load: %v %v", err, warnings)
	}
	a, _ := cfg.ResolveModel("a")
	if !a.XMLTools() || a.SystemPromptIn != "user" || a.SendStopSequences() {
		t.Errorf("a = %+v", a)
	}
	b, _ := cfg.ResolveModel("b")
	if b.XMLTools() || b.DecodeEntities != "auto" || b.SystemPromptIn != "user" {
		t.Errorf("b = %+v", b)
	}
	adhoc, _ := cfg.ResolveModel("gw/other")
	if !adhoc.XMLTools() {
		t.Errorf("ad-hoc model did not inherit: %+v", adhoc)
	}
	if cfg.Providers["gw"].RequestsPerMinute != 60 {
		t.Errorf("requests_per_minute = %d", cfg.Providers["gw"].RequestsPerMinute)
	}
}

func TestXMLToolOptionsValidated(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "config.toml"), `
[providers.gw]
base_url = "http://localhost:8080/v1"
tool_format = "yaml"

[[models]]
id = "a"
provider = "gw"
decode_entities = "true"
`)
	_, _, err := Load(Paths{Home: home})
	if err == nil || !strings.Contains(err.Error(), "tool_format") || !strings.Contains(err.Error(), "decode_entities") {
		t.Fatalf("err = %v", err)
	}
}
