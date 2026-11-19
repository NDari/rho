package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, tool Tool, env *Env, args any) Result {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Execute(context.Background(), env, raw, nil)
}

func text(r Result) string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestReadText(t *testing.T) {
	dir := t.TempDir()
	var content strings.Builder
	for i := 1; i <= 30; i++ {
		content.WriteString("line\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	env := &Env{Cwd: dir}
	r := run(t, Read{}, env, map[string]any{"path": "a.txt", "offset": 10, "limit": 5})
	if r.IsError {
		t.Fatal(text(r))
	}
	out := text(r)
	if !strings.Contains(out, "    10\tline") || strings.Contains(out, "    15\t") {
		t.Errorf("unexpected range:\n%s", out)
	}
	if !strings.Contains(out, "Use offset=15") {
		t.Errorf("missing continuation hint:\n%s", out)
	}
}

func TestReadBinaryAndMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	env := &Env{Cwd: dir}
	if r := run(t, Read{}, env, map[string]any{"path": "b.bin"}); !r.IsError || !strings.Contains(text(r), "binary") {
		t.Errorf("binary: %s", text(r))
	}
	if r := run(t, Read{}, env, map[string]any{"path": "nope"}); !r.IsError {
		t.Error("missing file should error")
	}
	if r := run(t, Read{}, env, map[string]any{"path": "x.png"}); !r.IsError {
		t.Error("missing image should error")
	}
}

func TestWriteCreatesAndDiffs(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Cwd: dir}
	r := run(t, Write{}, env, map[string]any{"path": "sub/new.txt", "content": "a\nb\n"})
	if r.IsError {
		t.Fatal(text(r))
	}
	if !r.Display.Diff.NewFile || r.Display.Diff.Added != 2 {
		t.Errorf("diff = %+v", r.Display.Diff)
	}
	r = run(t, Write{}, env, map[string]any{"path": "sub/new.txt", "content": "a\nc\n"})
	if r.IsError || r.Display.Diff.Added != 1 || r.Display.Diff.Removed != 1 {
		t.Errorf("overwrite diff = %+v (%s)", r.Display.Diff, text(r))
	}
	got, _ := os.ReadFile(filepath.Join(dir, "sub", "new.txt"))
	if string(got) != "a\nc\n" {
		t.Errorf("content = %q", got)
	}
}

func TestEdit(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		edits   []map[string]string
		want    string
		errSub  string
	}{
		{
			name:    "simple",
			initial: "one\ntwo\nthree\n",
			edits:   []map[string]string{{"old_text": "two", "new_text": "2"}},
			want:    "one\n2\nthree\n",
		},
		{
			name:    "crlf preserved",
			initial: "one\r\ntwo\r\nthree",
			edits:   []map[string]string{{"old_text": "one\ntwo", "new_text": "1\n2"}},
			want:    "1\r\n2\r\nthree",
		},
		{
			name:    "multiple sequential",
			initial: "a b c",
			edits:   []map[string]string{{"old_text": "a", "new_text": "x"}, {"old_text": "x b", "new_text": "y"}},
			want:    "y c",
		},
		{
			name:    "ambiguous",
			initial: "dup\ndup\n",
			edits:   []map[string]string{{"old_text": "dup", "new_text": "x"}},
			errSub:  "matches 2 times",
		},
		{
			name:    "whitespace hint",
			initial: "func f() {\n\treturn 1\n}\n",
			edits:   []map[string]string{{"old_text": "    return 1", "new_text": "\treturn 2"}},
			errSub:  "differs only in whitespace",
		},
		{
			name:    "not found leaves file unchanged",
			initial: "keep\n",
			edits:   []map[string]string{{"old_text": "keep", "new_text": "x"}, {"old_text": "zzz", "new_text": "y"}},
			errSub:  "not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "f.txt")
			if err := os.WriteFile(path, []byte(tt.initial), 0o644); err != nil {
				t.Fatal(err)
			}
			r := run(t, Edit{}, &Env{Cwd: dir}, map[string]any{"path": "f.txt", "edits": tt.edits})
			got, _ := os.ReadFile(path)
			if tt.errSub != "" {
				if !r.IsError || !strings.Contains(text(r), tt.errSub) {
					t.Fatalf("want error containing %q, got %q", tt.errSub, text(r))
				}
				if string(got) != tt.initial {
					t.Errorf("file changed on error: %q", got)
				}
				return
			}
			if r.IsError {
				t.Fatal(text(r))
			}
			if string(got) != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := findShell(""); err != nil {
			t.Skip("Git Bash not available")
		}
	}
	dir := t.TempDir()
	env := &Env{Cwd: dir, BashTimeout: 10}
	r := run(t, Bash{}, env, map[string]any{"command": "echo hello; echo err 1>&2; exit 3"})
	out := text(r)
	if !r.IsError || !strings.Contains(out, "hello") || !strings.Contains(out, "err") || !strings.Contains(out, "Exit code: 3") {
		t.Errorf("unexpected result: %q", out)
	}
	if *r.Display.ExitCode != 3 {
		t.Errorf("exit code = %d", *r.Display.ExitCode)
	}
}

func TestBashTruncatesKeepingTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := findShell(""); err != nil {
			t.Skip("Git Bash not available")
		}
	}
	env := &Env{Cwd: t.TempDir(), BashTimeout: 30}
	r := run(t, Bash{}, env, map[string]any{"command": "for i in $(seq 1 5000); do echo line$i; done"})
	out := text(r)
	if !strings.Contains(out, "line5000") || strings.Contains(out, "line1\n") {
		t.Errorf("tail not kept: %s", out[:200])
	}
	if !r.Display.Truncated || r.Display.FullOutputPath == "" {
		t.Fatalf("expected truncation with full output path")
	}
	full, err := os.ReadFile(r.Display.FullOutputPath)
	if err != nil || !strings.Contains(string(full), "line1\n") {
		t.Errorf("full output missing: %v", err)
	}
	_ = os.Remove(r.Display.FullOutputPath)
}

func TestBashCancelKillsTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := findShell(""); err != nil {
			t.Skip("Git Bash not available")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	env := &Env{Cwd: t.TempDir(), BashTimeout: 60}
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	r := Bash{}.Execute(ctx, env, json.RawMessage(`{"command":"sleep 30 & sleep 30; wait"}`), nil)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("cancel took too long: %v", time.Since(start))
	}
	if !r.IsError || !strings.Contains(text(r), "cancelled") {
		t.Errorf("result = %q", text(r))
	}
}

func TestBashTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := findShell(""); err != nil {
			t.Skip("Git Bash not available")
		}
	}
	env := &Env{Cwd: t.TempDir(), BashTimeout: 1}
	r := run(t, Bash{}, env, map[string]any{"command": "sleep 20"})
	if !r.IsError || !strings.Contains(text(r), "timed out") {
		t.Errorf("result = %q", text(r))
	}
}

func TestStripANSI(t *testing.T) {
	if got := StripANSI("\x1b[31mred\x1b[0m plain"); got != "red plain" {
		t.Errorf("StripANSI = %q", got)
	}
}
