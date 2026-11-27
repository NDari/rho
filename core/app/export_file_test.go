package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
	"github.com/NDari/rho/core/session"
	"github.com/NDari/rho/core/theme"
)

func TestDefaultExportName(t *testing.T) {
	// A session file whose header carries id, as older or other writers
	// may use ids of any length.
	withID := func(t *testing.T, id string) *session.Session {
		t.Helper()
		data, err := json.Marshal(session.Entry{ID: id, Type: session.TypeHeader, Version: session.FormatVersion})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "s.jsonl")
		writeFile(t, path, string(data)+"\n")
		sess, err := session.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		return sess
	}
	tests := []struct {
		id   string
		want string
	}{
		{"0123456789abcdef", "rho-01234567.html"},
		{"01234567", "rho-01234567.html"},
		{"abc", "rho-abc.html"},
	}
	for _, tc := range tests {
		if got := DefaultExportName(withID(t, tc.id)); got != tc.want {
			t.Errorf("DefaultExportName(id %q) = %q, want %q", tc.id, got, tc.want)
		}
	}
	fresh := session.New("", "", "test")
	if got := DefaultExportName(fresh); !strings.HasPrefix(got, "rho-") || !strings.HasSuffix(got, ".html") || !strings.Contains(got, fresh.ID()[:min(8, len(fresh.ID()))]) {
		t.Errorf("DefaultExportName(new session %s) = %q", fresh.ID(), got)
	}
}

func TestWriteExport(t *testing.T) {
	dir := t.TempDir()
	sess := session.New(dir, dir, "test")
	t.Cleanup(func() { _ = sess.Close() })
	if _, err := sess.AppendMessage(provider.UserText("export me")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "out.html")
	if err := WriteExport(path, sess, mustTheme(t, theme.DefaultLight)); err != nil {
		t.Fatal(err)
	}
	page := readFile(t, path)
	if !strings.Contains(page, "<title>export me</title>") {
		t.Errorf("page does not have the expected title:\n%.300s", page)
	}
	if strings.Contains(page, "prefers-color-scheme: dark") {
		t.Error("one theme was given, but the page has a dark variant")
	}
	if err := WriteExport(filepath.Join(dir, "missing", "out.html"), sess); err == nil {
		t.Error("writing into a missing directory succeeded")
	}
}

func TestAppExportHTML(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		path     func(cwd string) string // "" uses the default name
		wantFile func(a *App) string
		wantDark bool
	}{
		{"default name in the working directory", "", nil,
			func(a *App) string { return filepath.Join(a.Paths.Cwd, DefaultExportName(a.Agent.Session())) }, true},
		{"relative path is relative to the working directory", `{"theme": "github-light"}`,
			func(string) string { return "sub.html" },
			func(a *App) string { return filepath.Join(a.Paths.Cwd, "sub.html") }, false},
		{"absolute path", "", func(cwd string) string { return filepath.Join(cwd, "abs.html") },
			func(a *App) string { return filepath.Join(a.Paths.Cwd, "abs.html") }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t, tc.config, "", false, fake.Step{Text: "the **answer**"})
			if err := a.Agent.Run(context.Background(), provider.UserText("question one")); err != nil {
				t.Fatal(err)
			}
			arg := ""
			if tc.path != nil {
				arg = tc.path(a.Paths.Cwd)
			}
			got, err := a.ExportHTML(arg)
			if err != nil {
				t.Fatal(err)
			}
			if want := tc.wantFile(a); got != want {
				t.Errorf("wrote %q, want %q", got, want)
			}
			data, err := os.ReadFile(got)
			if err != nil {
				t.Fatal(err)
			}
			page := string(data)
			for _, want := range []string{"<title>question one</title>", "<strong>answer</strong>", a.Agent.Session().ID()} {
				if !strings.Contains(page, want) {
					t.Errorf("page does not contain %q", want)
				}
			}
			if dark := strings.Contains(page, "prefers-color-scheme: dark"); dark != tc.wantDark {
				t.Errorf("dark variant = %v, want %v (theme %q)", dark, tc.wantDark, a.Agent.Config().Theme)
			}
		})
	}
}

func mustTheme(t *testing.T, name string) theme.Theme {
	t.Helper()
	th, ok := theme.Builtin().Get(name)
	if !ok {
		t.Fatalf("no built-in theme %q", name)
	}
	return th
}
