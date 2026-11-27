package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateExample(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".rho")
	p := Paths{Home: home}
	if err := CreateExample(p.GlobalFile()); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := Load(p)
	if err != nil || len(warnings) > 0 {
		t.Fatalf("example does not load cleanly: %v %v", err, warnings)
	}
	m, err := cfg.ResolveModel("")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "local" || cfg.Providers["local"].BaseURL != "http://localhost:9931/v1" {
		t.Fatalf("unexpected model %+v", m)
	}

	// It never replaces an existing file.
	write(t, p.GlobalFile(), `{"theme": "light"}`)
	if err := CreateExample(p.GlobalFile()); err == nil {
		t.Fatal("expected an error for an existing file")
	}
	data, _ := os.ReadFile(p.GlobalFile())
	if string(data) != `{"theme": "light"}` {
		t.Fatalf("file was changed: %q", data)
	}

	// A legacy config.toml is converted instead.
	home = t.TempDir()
	p = Paths{Home: home}
	write(t, filepath.Join(home, legacyFileName), "theme = \"nord\"\n")
	if err := CreateExample(p.GlobalFile()); err == nil || !strings.Contains(err.Error(), "converted") {
		t.Fatalf("legacy file: %v", err)
	}
	if cfg, _, err := Load(p); err != nil || cfg.Theme != "nord" {
		t.Fatalf("converted file: %v %q", err, cfg.Theme)
	}
}

func TestNoModel(t *testing.T) {
	if _, err := Defaults().ResolveModel(""); !errors.Is(err, ErrNoModel) {
		t.Fatalf("got %v, want ErrNoModel", err)
	}
}

func TestUpdateCheckSetting(t *testing.T) {
	tests := []struct {
		file string
		want bool
	}{
		{"", true},
		{`{"update_check": true}`, true},
		{`{"update_check": false}`, false},
	}
	for _, tt := range tests {
		home := t.TempDir()
		write(t, filepath.Join(home, FileName), tt.file)
		cfg, _, err := Load(Paths{Home: home})
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.UpdateCheckEnabled(); got != tt.want {
			t.Errorf("%q: UpdateCheckEnabled() = %v, want %v", tt.file, got, tt.want)
		}
	}
}
