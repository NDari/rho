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
	if len(skills) != 2 || skills[0].Name != "pdf" || skills[1].Name != "rho-extensions" || len(warnings) != 1 {
		t.Errorf("skills=%+v warnings=%v", skills, warnings)
	}
	write(t, filepath.Join(p.Home, "skills", "rho-extensions", "SKILL.md"), "---\nname: rho-extensions\ndescription: mine\n---\nBody")
	if skills, _ := LoadSkills(p); len(skills) != 2 || skills[1].Description != "mine" {
		t.Errorf("a global skill should override the built-in one: %+v", skills)
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

func TestBuiltinPromptFollowsTools(t *testing.T) {
	specs := func(names ...string) []provider.ToolSpec {
		var out []provider.ToolSpec
		for _, n := range names {
			out = append(out, provider.ToolSpec{Name: n})
		}
		return out
	}
	const intro = "You are Rho, a coding agent working in the user's terminal. You help with software engineering tasks: understanding code, making changes, running commands, and debugging.\n\nGuidelines:\n"
	const edit = "- Read files before editing them. Use edit for targeted changes and write for new files or complete rewrites.\n- Keep changes focused on what was asked. Match the existing style of the code.\n"
	const verify = "- Verify your work when practical (build, run tests), and report failures honestly.\n"
	const tail = "- When requirements are unclear, ask instead of guessing.\n- Be concise. Refer to code by path, as path:line when a tool showed you the line number."
	tests := []struct {
		name  string
		tools []provider.ToolSpec
		want  string
	}{
		{"default tools", specs("read", "write", "edit", "bash"),
			intro + edit + "- Use bash to search (rg, grep, find), build, test, and inspect git state. Prefer non-interactive commands.\n" + verify + tail},
		{"with search tools", specs("read", "write", "edit", "bash", "grep", "find", "ls"),
			intro + edit + "- Use grep, find and ls to search and list files, and bash to build, test, and inspect git state. Prefer non-interactive commands.\n" + verify + tail},
		{"read-only", specs("read", "grep", "find", "ls"),
			intro + "- Use grep, find and ls to search and list files.\n" + tail},
		{"read only", specs("read"), intro + tail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := builtin(tt.tools); got != tt.want {
				t.Errorf("got\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestBuildIgnoresProjectAndUsesPromptFiles(t *testing.T) {
	p := testPaths(t)
	write(t, filepath.Join(p.Home, "AGENTS.md"), "global agents")
	write(t, filepath.Join(p.ProjectRoot, "AGENTS.md"), "project agents")
	write(t, filepath.Join(p.ProjectDir, "SYSTEM.md"), "project system")
	write(t, filepath.Join(p.ProjectDir, "APPEND_SYSTEM.md"), "project append")
	write(t, filepath.Join(p.Home, "APPEND_SYSTEM.md"), "global append")
	sys := filepath.Join(t.TempDir(), "reviewer.md")
	extra := filepath.Join(t.TempDir(), "extra.md")
	write(t, sys, "reviewer system")
	write(t, extra, "extra rules")

	p.NoProject = true
	b, err := Build(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"project agents", "project system", "project append"} {
		if strings.Contains(b.Text, s) {
			t.Errorf("project file loaded with NoProject: %q", s)
		}
	}
	for _, s := range []string{"global agents", "global append", "You are Rho"} {
		if !strings.Contains(b.Text, s) {
			t.Errorf("missing %q", s)
		}
	}

	p.NoProject = false
	b, err = Build(p, Options{SystemFile: sys, AppendFiles: []string{extra}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.Text, "project system") || !strings.HasPrefix(b.Text, "reviewer system") {
		t.Errorf("--system-file did not replace SYSTEM.md:\n%s", b.Text)
	}
	if i, j := strings.Index(b.Text, "project append"), strings.Index(b.Text, "extra rules"); i < 0 || j < i {
		t.Errorf("--append-system-file should follow APPEND_SYSTEM.md:\n%s", b.Text)
	}
	if _, err := Build(p, Options{SystemFile: filepath.Join(t.TempDir(), "missing.md")}); err == nil {
		t.Error("a missing --system-file should be an error")
	}
}
