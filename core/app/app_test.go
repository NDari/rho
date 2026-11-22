package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		{"config", "[tools]\nenabled = [\"read\", \"bash\"]\ndisabled = [\"bash\"]\n", nil, "read,grep,find,ls", false},
		{"flag replaces enabled", "[tools]\nenabled = [\"read\", \"bash\"]\n", ParseToolList("read, grep"), "read,grep,find,ls", false},
		{"flag keeps disabled", "[tools]\ndisabled = [\"ls\"]\n", ParseToolList("read,bash,ls"), "read,bash", false},
		{"unknown tool", "", ParseToolList("read,shell"), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("RHO_HOME", home)
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(tt.config), 0o644); err != nil {
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
