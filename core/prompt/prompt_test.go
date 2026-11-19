package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/provider"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testPaths(t *testing.T) config.Paths {
	home := t.TempDir()
	root := t.TempDir()
	cwd := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	return config.Paths{Home: home, Cwd: cwd, ProjectRoot: root, ProjectDir: filepath.Join(root, ".rho")}
}

func TestAgentsFilesOrder(t *testing.T) {
	p := testPaths(t)
	write(t, filepath.Join(p.Home, "AGENTS.md"), "global")
	write(t, filepath.Join(p.ProjectRoot, "AGENTS.md"), "root")
	write(t, filepath.Join(p.Cwd, "AGENTS.md"), "leaf")
	files, err := AgentsFiles(p)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Text)
	}
	if strings.Join(got, ",") != "global,root,leaf" {
		t.Errorf("order = %v", got)
	}
}

func TestBuildUsesSystemOverrideAndAppend(t *testing.T) {
	p := testPaths(t)
	write(t, filepath.Join(p.Home, "SYSTEM.md"), "global system")
	write(t, filepath.Join(p.ProjectDir, "SYSTEM.md"), "project system")
	write(t, filepath.Join(p.Home, "APPEND_SYSTEM.md"), "appended")
	b, err := Build(p, Options{Tools: []provider.ToolSpec{{Name: "read", Description: "Read a file. More."}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.Text, "project system") || strings.Contains(b.Text, "global system") {
		t.Errorf("project SYSTEM.md should win:\n%s", b.Text)
	}
	if !strings.Contains(b.Text, "appended") || !strings.Contains(b.Text, "- read: Read a file.") {
		t.Errorf("missing parts:\n%s", b.Text)
	}
	if strings.Contains(b.Text, "You are Rho") {
		t.Error("built-in prompt should be replaced")
	}
}

func TestBuiltinPromptIsShort(t *testing.T) {
	p := testPaths(t)
	b, err := Build(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if EstimateTokens(b.Text) > 1000 {
		t.Errorf("built-in prompt too long: %d tokens", EstimateTokens(b.Text))
	}
}

func TestSkillsAndTemplates(t *testing.T) {
	p := testPaths(t)
	write(t, filepath.Join(p.Home, "skills", "pdf", "SKILL.md"), "---\nname: pdf\ndescription: Work with PDF files\n---\nBody")
	write(t, filepath.Join(p.ProjectDir, "skills", "bad", "SKILL.md"), "---\nname: bad\n---\nno description")
	write(t, filepath.Join(p.ProjectDir, "prompts", "review.md"), "---\ndescription: Review code\n---\nReview $1 focusing on $2. All: $ARGS")
	skills, warnings := LoadSkills(p)
	if len(skills) != 1 || skills[0].Name != "pdf" || len(warnings) != 1 {
		t.Errorf("skills=%+v warnings=%v", skills, warnings)
	}
	tpls, _ := LoadTemplates(p)
	if len(tpls) != 1 || tpls[0].Description != "Review code" {
		t.Fatalf("templates = %+v", tpls)
	}
	got := tpls[0].Expand(`main.go "error handling"`)
	want := `Review main.go focusing on error handling. All: main.go "error handling"`
	if got != want {
		t.Errorf("Expand = %q, want %q", got, want)
	}
}

func TestExpandAppendsArgsWithoutPlaceholders(t *testing.T) {
	tpl := Template{Body: "Explain this."}
	if got := tpl.Expand("foo"); got != "Explain this.\n\nfoo" {
		t.Errorf("Expand = %q", got)
	}
}
