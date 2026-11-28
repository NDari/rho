package tools

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeTree creates files under dir; a name ending in / is a directory.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// searchRepo is a repository with ignore files at several levels.
func searchRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		".git/HEAD":            "needle in git\n",
		".git/info/exclude":    "excluded.txt\n",
		".gitignore":           "*.log\nbuild/\n!keep.log\n",
		".github/ci.yml":       "needle hidden\n",
		"a.go":                 "package a\n// needle one\n",
		"excluded.txt":         "needle excluded\n",
		"debug.log":            "needle log\n",
		"keep.log":             "needle kept\n",
		"build/out.go":         "needle build\n",
		"bin.dat":              "needle\x00binary\n",
		"sub/.ignore":          "secret.go\n",
		"sub/b.go":             "needle two\r\nplain\r\n",
		"sub/x.log":            "needle sub log\n",
		"sub/secret.go":        "needle secret\n",
		"sub/node/.gitignore":  "*.tmp\n",
		"sub/node/c.tmp":       "needle tmp\n",
		"sub/node/c.go":        "needle three\n",
		"sub/build/nested.txt": "needle nested build\n",
	})
	return dir
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestGrep(t *testing.T) {
	dir := searchRepo(t)
	tests := []struct {
		name string
		cwd  string
		args map[string]any
		want []string
	}{
		{"ignore files, hidden files, binary and .git", "", map[string]any{"pattern": "needle"}, []string{
			".github/ci.yml:1:needle hidden",
			"a.go:2:// needle one",
			"keep.log:1:needle kept",
			"sub/b.go:1:needle two",
			"sub/node/c.go:1:needle three",
		}},
		{"root rules apply below a subdirectory", "sub", map[string]any{"pattern": "needle"}, []string{
			"b.go:1:needle two",
			"node/c.go:1:needle three",
		}},
		{"glob on names", "", map[string]any{"pattern": "needle", "glob": "*.go"}, []string{
			"a.go:2:// needle one",
			"sub/b.go:1:needle two",
			"sub/node/c.go:1:needle three",
		}},
		{"glob on paths", "", map[string]any{"pattern": "needle", "glob": "sub/**/*.go"}, []string{
			"sub/b.go:1:needle two",
			"sub/node/c.go:1:needle three",
		}},
		{"path to a file is searched as given", "", map[string]any{"pattern": "needle", "path": "debug.log"}, []string{
			"debug.log:1:needle log",
		}},
		{"ignore case and literal", "", map[string]any{"pattern": "NEEDLE T", "ignore_case": true, "literal": true}, []string{
			"sub/b.go:1:needle two",
			"sub/node/c.go:1:needle three",
		}},
		{"limit", "", map[string]any{"pattern": "needle", "limit": 2}, []string{
			".github/ci.yml:1:needle hidden",
			"a.go:2:// needle one",
			"",
			"[Stopped at 2 matches. Narrow the search with path or glob, or raise limit.]",
		}},
		{"context", "", map[string]any{"pattern": "needle (one|two)", "context": 1}, []string{
			"a.go-1-package a",
			"a.go:2:// needle one",
			"--",
			"sub/b.go:1:needle two",
			"sub/b.go-2-plain",
		}},
		{"no matches", "", map[string]any{"pattern": "absent"}, []string{"no matches"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := run(t, Grep{}, &Env{Cwd: filepath.Join(dir, tt.cwd)}, tt.args)
			if r.IsError {
				t.Fatal(text(r))
			}
			if got := lines(text(r)); !slices.Equal(got, tt.want) {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestGrepErrorsAndLongLines(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("x", 2000) + "needle" + strings.Repeat("y", 2000)
	writeTree(t, dir, map[string]string{"long.txt": long + "\n"})
	env := &Env{Cwd: dir}
	r := run(t, Grep{}, env, map[string]any{"pattern": "needle"})
	out := text(r)
	if r.IsError || !strings.Contains(out, "needle") || len(out) > GrepMaxLineLen+50 {
		t.Errorf("long line not cut around the match (%d bytes):\n%s", len(out), out)
	}
	for _, args := range []map[string]any{
		{"pattern": "("},
		{"pattern": "x", "path": "missing"},
		{"pattern": "x", "glob": "["},
		{},
	} {
		if r := run(t, Grep{}, env, args); !r.IsError {
			t.Errorf("%v: want an error, got %s", args, text(r))
		}
	}
}

func TestFind(t *testing.T) {
	dir := searchRepo(t)
	tests := []struct {
		name string
		cwd  string
		args map[string]any
		want []string
	}{
		{"names at any depth", "", map[string]any{"pattern": "*.go"}, []string{"a.go", "sub/b.go", "sub/node/c.go"}},
		{"directories end in a slash", "", map[string]any{"pattern": "node"}, []string{"sub/node/"}},
		{"path pattern relative to path", "", map[string]any{"pattern": "node/*", "path": "sub"}, []string{"sub/node/.gitignore", "sub/node/c.go"}},
		{"ignored paths and .git are skipped", "", map[string]any{"pattern": "*.{log,txt,tmp}"}, []string{"keep.log"}},
		{"hidden files are included", "", map[string]any{"pattern": ".github/*"}, []string{".github/ci.yml"}},
		{"limit", "", map[string]any{"pattern": "*.go", "limit": 1}, []string{"a.go", "", "[Stopped at 1 path. Narrow the pattern or path, or raise limit.]"}},
		{"no matches", "", map[string]any{"pattern": "*.rs"}, []string{"no matching paths"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := run(t, Find{}, &Env{Cwd: filepath.Join(dir, tt.cwd)}, tt.args)
			if r.IsError {
				t.Fatal(text(r))
			}
			if got := lines(text(r)); !slices.Equal(got, tt.want) {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
	if r := run(t, Find{}, &Env{Cwd: dir}, map[string]any{"pattern": "*", "path": "a.go"}); !r.IsError {
		t.Errorf("a file as path: want an error, got %s", text(r))
	}
}

func TestLs(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"b.txt": "", "a/": "", ".hidden": "", "empty/": ""})
	symlink := os.Symlink(filepath.Join(dir, "b.txt"), filepath.Join(dir, "c")) == nil
	env := &Env{Cwd: dir}
	want := []string{".hidden", "a/", "b.txt"}
	if symlink {
		want = append(want, "c@")
	}
	want = append(want, "empty/")
	if got := lines(text(run(t, Ls{}, env, map[string]any{}))); !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	got := lines(text(run(t, Ls{}, env, map[string]any{"limit": 2})))
	if len(got) != 4 || !strings.HasPrefix(got[3], "[Showing 2 of ") {
		t.Errorf("limit: got %q", got)
	}
	if got := text(run(t, Ls{}, env, map[string]any{"path": "empty"})); got != "(empty directory)" {
		t.Errorf("empty: got %q", got)
	}
	if r := run(t, Ls{}, env, map[string]any{"path": "b.txt"}); !r.IsError {
		t.Errorf("a file as path: want an error, got %s", text(r))
	}
}

func TestSelect(t *testing.T) {
	tests := []struct {
		name              string
		enabled, disabled []string
		want, wantAuto    string
		wantErr           bool
	}{
		{"default", nil, nil, "read,write,edit,bash", "", false},
		{"disabled", nil, []string{"write"}, "read,edit,bash", "", false},
		{"no bash adds search tools", []string{"read"}, nil, "read,grep,find,ls", "grep,find,ls", false},
		{"disabling bash adds search tools", nil, []string{"bash"}, "read,write,edit,grep,find,ls", "grep,find,ls", false},
		{"search tools can be disabled", []string{"read"}, []string{"ls"}, "read,grep,find", "grep,find", false},
		{"listed search tools are not automatic", []string{"read", "grep"}, nil, "read,grep,find,ls", "find,ls", false},
		{"enabled with bash", []string{"bash", "grep", "read"}, nil, "read,bash,grep", "", false},
		{"empty", []string{}, nil, "grep,find,ls", "grep,find,ls", false},
		{"unknown", []string{"reed"}, nil, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, auto, err := Select(tt.enabled, tt.disabled)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if got := strings.Join(Names(ts), ","); got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
			if got := strings.Join(auto, ","); got != tt.wantAuto {
				t.Errorf("auto = %s, want %s", got, tt.wantAuto)
			}
		})
	}
}

func TestGitIgnored(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{
		".git/HEAD":         "ref: refs/heads/main\n",
		".gitignore":        "/private/\n",
		".ignore":           ".rho/\n", // git doesn't read it
		"sub/.gitignore":    "*.local\n",
		"private/a.jsonc":   "",
		".rho/config.jsonc": "",
		"sub/x.local":       "",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for path, want := range map[string]bool{
		"private/a.jsonc":   true,
		".rho/config.jsonc": false,
		"sub/x.local":       true,
		"sub/y.jsonc":       false,
	} {
		in, got := GitIgnored(filepath.Join(root, path))
		if !in || got != want {
			t.Errorf("%s: in repo %v, ignored %v, want ignored %v", path, in, got, want)
		}
	}
	if in, _ := GitIgnored(filepath.Join(t.TempDir(), "a")); in {
		t.Error("a directory outside a repository counts as in one")
	}
}
