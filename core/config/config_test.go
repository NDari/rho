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
