package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTopicsAndFiles(t *testing.T) {
	for _, tp := range Topics {
		data, err := Read(tp.Name)
		if err != nil || len(data) == 0 {
			t.Errorf("topic %s: %v", tp.Name, err)
		}
	}
	if data, err := Read("examples/extensions/guard.go"); err != nil || !strings.Contains(string(data), "package main") {
		t.Errorf("example: %v", err)
	}
	if _, err := Read("nope"); err == nil {
		t.Error("an unknown topic should be an error")
	}
	if len(Examples()) == 0 {
		t.Error("no examples embedded")
	}
}

func TestInstall(t *testing.T) {
	home := t.TempDir()
	old := filepath.Join(home, "docs", "000000000000")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := Install(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an older copy was not removed")
	}
	for _, rel := range []string{"extensions.md", "configuration.md", "rho-spec.md", "examples/extensions/guard.go", "skills/rho-extensions/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s not installed: %v", rel, err)
		}
	}
	skills := Skills(dir)
	path, ok := skills["rho-extensions"]
	if !ok {
		t.Fatalf("skills = %v", skills)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), placeholder) || !strings.Contains(string(data), filepath.ToSlash(dir)+"/extensions.md") {
		t.Errorf("placeholder not filled in:\n%s", data)
	}
	guide, err := os.ReadFile(filepath.Join(dir, "extensions.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(guide), "](../examples/") || !strings.Contains(string(guide), "](examples/") {
		t.Error("links to the examples were not fixed for the flat layout")
	}
	if again, err := Install(home); err != nil || again != dir {
		t.Errorf("second install = %q, %v", again, err)
	}
}
