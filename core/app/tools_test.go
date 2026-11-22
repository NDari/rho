package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/tools"
)

// searchExtension provides a grep tool, which may replace the built-in
// one, and a word_count tool.
const searchExtension = `package main

import "rho"

func main() {
	rho.Info("search", "0.1.0")
	for _, name := range []string{"grep", "word_count"} {
		rho.RegisterTool(rho.Tool{
			Name:        name,
			Description: "Extension " + name,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			ReadOnly:    true,
			Execute: func(ctx *rho.ToolContext, args map[string]any) (string, error) {
				return "from the extension", nil
			},
		})
	}
}
`

type quietUI struct{}

func (quietUI) Confirm(context.Context, string, string) (bool, error)    { return false, nil }
func (quietUI) Select(context.Context, string, []string) (string, error) { return "", nil }
func (quietUI) Input(context.Context, string, string) (string, error)    { return "", nil }
func (quietUI) Notify(string, string)                                    {}
func (quietUI) SetStatus(string, string)                                 {}

func TestExtensionToolsAndToolSelection(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		tools   []string
		want    string // tools offered; grep* is the extension's grep
		notice  string // a notice that must be shown
		listing string // a line /extensions must show
	}{
		{"extension replaces an automatic search tool", "", ParseToolList("read"),
			"read,find,ls,grep*,word_count", "an extension's grep tool replaces the built-in grep", "  tool: grep\n"},
		{"a listed built-in wins", "", ParseToolList("read,grep"),
			"read,grep,find,ls,word_count", "", ""},
		{"disabled extension tool and a typo", "[tools]\ndisabled = [\"word_count\", \"bassh\"]\n", nil,
			"read,write,edit,bash,grep*", `tools.disabled: no tool named "bassh" is loaded`, "  tool: word_count (disabled in tools.disabled)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("RHO_HOME", home)
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(tt.config), 0o644); err != nil {
				t.Fatal(err)
			}
			extDir := filepath.Join(home, "extensions", "search")
			if err := os.MkdirAll(extDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(extDir, "search.go"), []byte(searchExtension), 0o644); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var notices []string
			sink := func(e events.Event) {
				if n, ok := e.(events.Notice); ok {
					mu.Lock()
					notices = append(notices, n.Text)
					mu.Unlock()
				}
			}
			a, err := New(Options{Cwd: t.TempDir(), Model: "openai/test-model", Tools: tt.tools, Sink: sink})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.Agent.Session().Close() })
			a.StartExtensions(context.Background(), quietUI{})
			t.Cleanup(a.StopExtensions)

			var names []string
			for _, tl := range a.Agent.Tools() {
				n := tl.Spec().Name
				if _, builtin := tl.(tools.Grep); n == "grep" && !builtin {
					n += "*"
				}
				names = append(names, n)
			}
			if got := strings.Join(names, ","); got != tt.want {
				t.Errorf("tools = %s, want %s (%s)", got, tt.want, a.Ext.Describe())
			}
			mu.Lock()
			all := strings.Join(notices, "\n")
			mu.Unlock()
			if tt.notice != "" && !strings.Contains(all, tt.notice) {
				t.Errorf("notices %q do not include %q", all, tt.notice)
			}
			if strings.Contains(all, `"word_count"`) {
				t.Errorf("a disabled extension tool was reported as unknown: %q", all)
			}
			if tt.listing != "" && !strings.Contains(a.Ext.Describe(), tt.listing) {
				t.Errorf("/extensions:\n%s\nwant a line %q", a.Ext.Describe(), tt.listing)
			}
		})
	}
}

func TestUnknownDisabledWithoutExtensions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RHO_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[tools]\ndisabled = [\"bassh\", \"write\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{Cwd: t.TempDir(), Model: "openai/test-model", NoExtensions: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Agent.Session().Close() })
	w := strings.Join(a.Warnings, "\n")
	if !strings.Contains(w, `"bassh"`) || strings.Contains(w, `"write"`) {
		t.Errorf("warnings = %q", w)
	}
}
