package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NDari/rho/core/compact"
	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/prompt"
	"github.com/NDari/rho/core/provider"
)

func TestForkCopiesLabels(t *testing.T) {
	t.Setenv("RHO_HOME", t.TempDir())
	a, err := New(Options{Cwd: t.TempDir(), Model: "openai/test-model", NoExtensions: true})
	if err != nil {
		t.Fatal(err)
	}
	src := a.Agent.Session()
	t.Cleanup(func() { _ = src.Close() }) // Windows cannot remove open files
	var ids []string
	for _, text := range []string{"one", "two", "three"} {
		e, err := src.AppendMessage(provider.UserText(text))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	for id, label := range map[string]string{ids[0]: "start", ids[2]: "beyond"} {
		if err := a.Agent.Label(id, label); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		at       string
		leafWant string // prefix of the label on the fork point
	}{
		{"unlabeled fork point", ids[1], "forked from "},
		{"labeled fork point", ids[0], "start"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dst, err := a.Fork(tc.at)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = dst.Close() })
			labels := dst.Labels()
			first := dst.PathTo(dst.Leaf())[0]
			if labels[first.ID] != "start" {
				t.Errorf("label on the first entry = %q, want %q", labels[first.ID], "start")
			}
			for _, l := range labels {
				if l == "beyond" {
					t.Error("a label from beyond the fork point was copied")
				}
			}
			if got := labels[dst.Leaf()]; !strings.HasPrefix(got, tc.leafWant) {
				t.Errorf("fork point label = %q, want prefix %q", got, tc.leafWant)
			}
		})
	}
}

func TestToolSelection(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		tools   []string
		want    string
		wantErr bool
	}{
		{"default", "", nil, "read,write,edit,bash", false},
		{"config", `{"tools": {"enabled": ["read", "bash"], "disabled": ["bash"]}}`, nil, "read,grep,find,ls", false},
		{"flag replaces enabled", `{"tools": {"enabled": ["read", "bash"]}}`, ParseToolList("read, grep"), "read,grep,find,ls", false},
		{"flag keeps disabled", `{"tools": {"disabled": ["ls"]}}`, ParseToolList("read,bash,ls"), "read,bash", false},
		{"unknown tool", "", ParseToolList("read,shell"), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("RHO_HOME", home)
			if err := os.WriteFile(filepath.Join(home, config.FileName), []byte(tt.config), 0o644); err != nil {
				t.Fatal(err)
			}
			a, err := New(Options{Cwd: t.TempDir(), Model: "openai/test-model", NoExtensions: true, Tools: tt.tools})
			if tt.wantErr {
				if err == nil {
					t.Fatal("want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.Agent.Session().Close() })
			var names []string
			for _, tl := range a.Agent.Tools() {
				names = append(names, tl.Spec().Name)
			}
			if got := strings.Join(names, ","); got != tt.want {
				t.Errorf("tools = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestNoProjectContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RHO_HOME", home)
	proj := t.TempDir()
	files := map[string]string{
		filepath.Join(home, config.FileName):                         `{"theme": "dark"}`,
		filepath.Join(proj, ".git", "HEAD"):                          "",
		filepath.Join(proj, ".rho", config.FileName):                 `{"theme": "light"}`,
		filepath.Join(proj, ".rho", "skills", "evil", "SKILL.md"):    "---\nname: evil\ndescription: d\n---\nbody\n",
		filepath.Join(proj, ".rho", "prompts", "evil.md"):            "evil prompt\n",
		filepath.Join(proj, ".rho", "extensions", "evil", "evil.go"): "package main\n",
		filepath.Join(proj, ".rho", "COMPACT.md"):                    "evil compaction\n",
		filepath.Join(proj, "AGENTS.md"):                             "evil agents\n",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, ignore := range []bool{false, true} {
		a, err := New(Options{Cwd: proj, Model: "openai/test-model", NoProjectContext: ignore})
		if err != nil {
			t.Fatal(err)
		}
		cands, _ := a.Ext.Discover()
		loaded := map[string]bool{
			"config":     a.Config.Theme == "light",
			"skills":     slices.ContainsFunc(a.Skills, func(s prompt.Skill) bool { return s.Name == "evil" }),
			"prompts":    len(a.Templates) > 0,
			"extensions": len(cands) > 0,
			"compaction": strings.Contains(compact.Instructions(a.Paths), "evil"),
		}
		files, err := prompt.AgentsFiles(a.Paths)
		if err != nil {
			t.Fatal(err)
		}
		loaded["AGENTS.md"] = len(files) > 0
		for what, got := range loaded {
			if got == ignore {
				t.Errorf("NoProjectContext=%v: project %s loaded=%v", ignore, what, got)
			}
		}
		_ = a.Agent.Session().Close()
	}
	if _, err := New(Options{Cwd: proj, Model: "openai/test-model", NoExtensions: true, SystemFile: filepath.Join(proj, "missing.md")}); err == nil {
		t.Error("a missing --system-file should fail at start")
	}
}
