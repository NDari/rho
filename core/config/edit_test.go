package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const editGlobal = `// mine
{
	"default_model": "a",
	"providers": {
		// the local server
		"local": {
			"base_url": "http://localhost:9931/v1", // tunnel
			"models": [
				// first
				{"id": "a", "context_window": 8192},
				{"id": "b"},
			],
		},
	},
}
`

// editPaths returns paths with the global file holding global and, when
// project is not "", a project file holding it.
func editPaths(t *testing.T, global, project string) Paths {
	t.Helper()
	dir := t.TempDir()
	p := Paths{Home: filepath.Join(dir, "home"), Cwd: filepath.Join(dir, "proj"), ProjectRoot: filepath.Join(dir, "proj"), ProjectDir: filepath.Join(dir, "proj", ".rho")}
	for path, data := range map[string]string{p.GlobalFile(): global, p.ProjectFile(): project} {
		if data == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestEditFile(t *testing.T) {
	tests := []struct {
		name    string
		project bool
		edits   []Edit
		// want lists strings the saved file must hold, gone strings it
		// must not.
		want, gone []string
		err        string
	}{
		{name: "set keeps comments", edits: []Edit{{Kind: EditSet, Key: "bash.timeout", Value: 45.0}},
			want: []string{"// mine", "// tunnel", "// first", `"timeout": 45`}},
		{name: "unset", edits: []Edit{{Kind: EditSet, Key: "default_model"}}, gone: []string{"default_model"}},
		{name: "unknown setting", edits: []Edit{{Kind: EditSet, Key: "colour", Value: "x"}}, err: `unknown setting "colour"`},
		{name: "bad value", edits: []Edit{{Kind: EditSet, Key: "bash.timeout", Value: 1.5}}, err: "whole number"},
		{name: "unknown default model", edits: []Edit{{Kind: EditSet, Key: "default_model", Value: "zz"}}, err: `default_model "zz" is not a configured model`},
		{name: "rename provider", edits: []Edit{{Kind: EditProvider, Provider: "local", Name: "mac",
			Fields: map[string]any{"base_url": "http://localhost:9931/v1", "type": "anthropic"}}},
			want: []string{`"mac": {`, "// the local server", "// tunnel", "// first", `"type":`}, gone: []string{`"local"`}},
		{name: "provider name taken", edits: []Edit{
			{Kind: EditProvider, Name: "other", Fields: map[string]any{"base_url": "http://x"}},
			{Kind: EditProvider, Provider: "other", Name: "local", Fields: map[string]any{"base_url": "http://x"}},
		}, err: `there is already a provider named "local"`},
		{name: "new provider needs a URL", edits: []Edit{{Kind: EditProvider, Name: "other", Fields: map[string]any{"type": "anthropic"}}},
			err: `provider "other": base_url is required`},
		{name: "built-in provider with no settings", edits: []Edit{{Kind: EditProvider, Name: "anthropic"}},
			want: []string{`"anthropic": {}`}},
		{name: "edit model keeps the rest", edits: []Edit{{Kind: EditModel, Provider: "local", Index: 0,
			Fields: map[string]any{"id": "a", "context_window": 8192.0, "max_output": 1024.0, "headers": "ignored"}}},
			want: []string{"// first", `"max_output":`, `"context_window":`}},
		{name: "rename model updates default", edits: []Edit{{Kind: EditModel, Provider: "local", Index: 0, Fields: map[string]any{"id": "c"}}},
			want: []string{`"default_model": "c"`, `"id": "c"`}, gone: []string{"8192"}},
		{name: "move model", edits: []Edit{{Kind: EditModel, Provider: "local", Name: "anthropic", Index: 1, Fields: map[string]any{"id": "b", "name": "claude"}}},
			want: []string{`"anthropic": {`, `"name": "claude"`}},
		{name: "duplicate model", edits: []Edit{{Kind: EditModel, Provider: "local", Index: -1, Fields: map[string]any{"id": "b"}}},
			err: `model "b" is listed twice under provider "local"`},
		{name: "new model", edits: []Edit{{Kind: EditModel, Provider: "anthropic", Index: -1,
			Fields: map[string]any{"id": "sonnet", "supports_thinking": true, "pricing": map[string]any{"input": 2.0, "output": 10.0}}}},
			want: []string{`"anthropic": {`, `"sonnet"`, `{"input": 2, "output": 10}`}},
		{name: "delete default model", edits: []Edit{{Kind: EditDeleteModel, Provider: "local", Index: 0}},
			err: `default_model "a" is not a configured model`},
		{name: "delete model", edits: []Edit{{Kind: EditDeleteModel, Provider: "local", Index: 1}}, gone: []string{`"b"`}},
		{name: "delete provider and default", edits: []Edit{{Kind: EditSet, Key: "default_model"}, {Kind: EditDeleteProvider, Provider: "local"}},
			err: "no model configured"},
		{name: "project model", project: true, edits: []Edit{{Kind: EditModel, Provider: "local", Index: -1, Fields: map[string]any{"id": "p"}}},
			want: []string{`"id": "p"`}},
		{name: "project provider setting", project: true, edits: []Edit{{Kind: EditProvider, Name: "local", Fields: map[string]any{"base_url": "http://evil"}}},
			err: "can add models to it but not change its other settings"},
		{name: "project new provider", project: true, edits: []Edit{{Kind: EditModel, Provider: "evil", Index: -1, Fields: map[string]any{"id": "p"}}},
			err: "a project config cannot add one"},
		{name: "project global-only setting", project: true, edits: []Edit{{Kind: EditSet, Key: "bash.shell", Value: "/bin/evil"}},
			err: "can only be set in the global config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := editPaths(t, editGlobal, "")
			path := p.GlobalFile()
			v := LoadView(p)
			version := v.Global.Version
			if tt.project {
				path, version = p.ProjectFile(), v.Project.Version
			}
			err := EditFile(p, tt.project, version, tt.edits)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				if data, _ := os.ReadFile(p.GlobalFile()); string(data) != editGlobal {
					t.Errorf("a refused edit changed the file:\n%s", data)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(string(data), w) {
					t.Errorf("file lacks %q:\n%s", w, data)
				}
			}
			for _, g := range tt.gone {
				if strings.Contains(string(data), g) {
					t.Errorf("file still has %q:\n%s", g, data)
				}
			}
			if _, _, err := Load(p); err != nil {
				t.Errorf("the saved config doesn't load: %v", err)
			}
		})
	}
}

func TestEditFileRefusesAStaleVersion(t *testing.T) {
	p := editPaths(t, editGlobal, "")
	v := LoadView(p)
	if err := os.WriteFile(p.GlobalFile(), []byte(editGlobal+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := EditFile(p, false, v.Global.Version, []Edit{{Kind: EditSet, Key: "theme", Value: "nord"}})
	if !errors.Is(err, ErrFileChanged) {
		t.Errorf("err = %v, want ErrFileChanged", err)
	}
}

func TestLoadView(t *testing.T) {
	p := editPaths(t, editGlobal, `{"bash": {"timeout": 5}}`)
	v := LoadView(p)
	if v.Error != "" || !v.Global.Exists || v.Project == nil || !v.Project.Exists {
		t.Fatalf("view = %+v", v)
	}
	if got := child(v.Project.Values, "bash", "timeout"); got != 5.0 {
		t.Errorf("project bash.timeout = %v", got)
	}
	if got := child(v.Effective, "bash", "timeout"); got != 5.0 {
		t.Errorf("effective bash.timeout = %v", got)
	}
	models, _ := child(v.Effective, "providers", "local", "models").([]any)
	if len(models) != 2 {
		t.Fatalf("effective models = %v", models)
	}
	if got := models[1].(map[string]any)["context_window"]; got != float64(DefaultContextWindow) {
		t.Errorf("default context window = %v", got)
	}
	if !strings.Contains(strings.Join(v.Builtin, ","), "anthropic") {
		t.Errorf("builtin = %v", v.Builtin)
	}

	broken := LoadView(editPaths(t, `{"default_model": "zz"}`, ""))
	if !strings.Contains(broken.Error, "zz") {
		t.Errorf("error = %q", broken.Error)
	}
}

func TestProviderFrom(t *testing.T) {
	p, err := ProviderFrom("anthropic", map[string]any{"base_url": "http://localhost:9931"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != ProviderAnthropic || p.APIKeyEnv != "ANTHROPIC_API_KEY" || p.BaseURL != "http://localhost:9931" {
		t.Errorf("provider = %+v", p)
	}
	if p, _ := ProviderFrom("new", nil); p.Type != ProviderOpenAICompat {
		t.Errorf("new provider type = %q", p.Type)
	}
}
