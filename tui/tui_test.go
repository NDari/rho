package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/NDari/rho/core/tools"
)

func TestStableSplit(t *testing.T) {
	tests := []struct {
		src  string
		want string
	}{
		{"para one\n\npara two", "para one\n\n"},
		{"no boundary yet", ""},
		{"```go\ncode\n\nmore\n```\n\nafter", "```go\ncode\n\nmore\n```\n\n"},
		{"```\nopen fence\n\nstill open", ""},
	}
	for _, tt := range tests {
		if got := tt.src[:stableSplit(tt.src)]; got != tt.want {
			t.Errorf("stableSplit(%q) prefix = %q, want %q", tt.src, got, tt.want)
		}
	}
}

func TestWordDiff(t *testing.T) {
	a, b := wordDiff("return a - b", "return a + b")
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("ranges = %v %v", a, b)
	}
	if "return a - b"[a[0].start:a[0].end] != "-" {
		t.Errorf("removed range = %v", a[0])
	}
	if a, b := wordDiff("completely different", "xyz 123 qq"); a != nil || b != nil {
		t.Error("dissimilar lines should not get emphasis")
	}
}

func TestDiffRenderFitsWidth(t *testing.T) {
	d := tools.MakeDiff("package main\n\nfunc add(a, b int) int { return a - b }\n", "package main\n\nfunc add(a, b int) int { return a + b }\n", 3)
	r := &diffRenderer{theme: DarkTheme, hl: newHighlighter(DarkTheme.Chroma), st: newStyles(DarkTheme)}
	for _, side := range []bool{false, true} {
		width := 80
		if side {
			width = 160
		}
		lines := r.Render(d, "x.go", width, side, 0)
		if len(lines) == 0 {
			t.Fatal("no output")
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("side=%v: line width %d > %d: %q", side, w, width, ansi.Strip(l))
			}
		}
		joined := ansi.Strip(strings.Join(lines, "\n"))
		if !strings.Contains(joined, "a + b") || !strings.Contains(joined, "a - b") {
			t.Errorf("side=%v: missing content:\n%s", side, joined)
		}
	}
}

func TestCompletion(t *testing.T) {
	cmds := []commandInfo{{"model", "switch"}, {"new", "new"}}
	c := computeCompletion("/mo", true, cmds, "")
	if c == nil || len(c.items) != 1 || c.items[0].title != "/model" {
		t.Fatalf("command completion = %+v", c)
	}
	if computeCompletion("/mo", false, cmds, "") != nil {
		t.Error("commands complete only on the first line")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c = computeCompletion("look at @src/ma", true, cmds, dir)
	if c == nil || len(c.items) != 1 || c.items[0].value != "@src/main.go " {
		t.Fatalf("file completion = %+v", c)
	}
}

func TestMarkdownRendersCode(t *testing.T) {
	md := newMarkdown(DarkTheme)
	lines := md.Render("# Title\n\nSome `code` here.\n\n```go\nfunc main() {}\n```", 60)
	joined := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "Title") || !strings.Contains(joined, "func main()") {
		t.Errorf("unexpected render:\n%s", joined)
	}
}

func TestHistory(t *testing.T) {
	dir := t.TempDir()
	h := loadHistory(dir)
	h.add("one")
	h.add("two")
	h2 := loadHistory(dir)
	if s, ok := h2.prev("draft"); !ok || s != "two" {
		t.Fatalf("prev = %q", s)
	}
	if s, _ := h2.prev(""); s != "one" {
		t.Errorf("prev = %q", s)
	}
	h2.next()
	if s, _ := h2.next(); s != "draft" {
		t.Errorf("draft not restored: %q", s)
	}
}

func TestPickerKeepsDetailForLongTitles(t *testing.T) {
	rc := &renderCtx{width: 60, theme: DarkTheme, st: newStyles(DarkTheme)}
	long := strings.Repeat("a very long message title ", 10)
	items := []pickerItem{{title: long, detail: "● current · Nov 19 12:00", value: "a"}, {title: long, detail: "│ Nov 19 11:00", value: "b"}}
	p := newPicker("Session tree", items, nil)
	for _, cursor := range []int{0, 1} {
		p.cursor = cursor
		lines := p.render(rc, 10)
		joined := ansi.Strip(strings.Join(lines, "\n"))
		if !strings.Contains(joined, "● current") {
			t.Errorf("cursor=%d: current marker missing:\n%s", cursor, joined)
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > rc.width {
				t.Errorf("cursor=%d: line width %d > %d: %q", cursor, w, rc.width, ansi.Strip(l))
			}
		}
	}
}
