// Package prompt builds the system prompt from the built-in prompt,
// SYSTEM.md / APPEND_SYSTEM.md, AGENTS.md files and skills.
package prompt

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/provider"
)

// Part is one contributor to the system prompt, reported by /context.
type Part struct {
	Name   string
	Path   string
	Tokens int
}

// Built is an assembled system prompt.
type Built struct {
	Text  string
	Parts []Part
}

// Options control prompt assembly.
type Options struct {
	Tools  []provider.ToolSpec
	Skills []Skill
	Date   time.Time
	// Aside marks a read-only side conversation.
	Aside bool
}

const builtin = `You are Rho, a coding agent working in the user's terminal. You help with software engineering tasks: understanding code, making changes, running commands, and debugging.

Guidelines:
- Read files before editing them. Use edit for targeted changes and write for new files or complete rewrites.
- Keep changes focused on what was asked. Match the existing style of the code.
- Use bash to search (rg, grep, find), build, test, and inspect git state. Prefer non-interactive commands.
- Verify your work when practical (build, run tests), and report failures honestly.
- When requirements are unclear, ask instead of guessing.
- Be concise. Refer to code as path:line.`

const asideNote = `You are in a side conversation. It shares the main conversation's context, but nothing here is carried back automatically. You can only read files in this mode: do not attempt to modify files or run commands.`

// Build assembles the system prompt.
func Build(p config.Paths, o Options) (Built, error) {
	var b Built
	var sb strings.Builder
	add := func(name, path, text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(text)
		b.Parts = append(b.Parts, Part{Name: name, Path: path, Tokens: EstimateTokens(text)})
	}

	if path, text, err := firstFile(filepath.Join(p.ProjectDir, "SYSTEM.md"), filepath.Join(p.Home, "SYSTEM.md")); err != nil {
		return b, err
	} else if path != "" {
		add("SYSTEM.md", path, text)
	} else {
		add("Built-in system prompt", "", builtin)
	}
	for _, path := range []string{filepath.Join(p.Home, "APPEND_SYSTEM.md"), filepath.Join(p.ProjectDir, "APPEND_SYSTEM.md")} {
		text, err := readOptional(path)
		if err != nil {
			return b, err
		}
		add("APPEND_SYSTEM.md", path, text)
	}
	if len(o.Tools) > 0 {
		add("Tool list", "", toolList(o.Tools))
	}
	if o.Aside {
		add("Side conversation note", "", asideNote)
	}
	add("Environment", "", environment(p, o.Date))

	files, err := AgentsFiles(p)
	if err != nil {
		return b, err
	}
	if len(files) > 0 {
		var ab strings.Builder
		ab.WriteString("# Project context\n\nThe following instruction files were loaded. Follow them.")
		for _, f := range files {
			fmt.Fprintf(&ab, "\n\n## %s\n\n%s", f.Path, strings.TrimSpace(f.Text))
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(ab.String())
		for _, f := range files {
			b.Parts = append(b.Parts, Part{Name: "AGENTS.md", Path: f.Path, Tokens: EstimateTokens(f.Text)})
		}
	}
	if len(o.Skills) > 0 {
		add("Skills", "", skillsSection(o.Skills))
	}
	b.Text = sb.String()
	return b, nil
}

func toolList(tools []provider.ToolSpec) string {
	var b strings.Builder
	b.WriteString("Available tools:")
	for _, t := range tools {
		desc := t.Description
		if i := strings.Index(desc, ". "); i >= 0 {
			desc = desc[:i+1]
		}
		fmt.Fprintf(&b, "\n- %s: %s", t.Name, desc)
	}
	return b.String()
}

func environment(p config.Paths, date time.Time) string {
	if date.IsZero() {
		date = time.Now()
	}
	var b strings.Builder
	b.WriteString("Environment:")
	fmt.Fprintf(&b, "\n- Working directory: %s", p.Cwd)
	if p.ProjectRoot != p.Cwd {
		fmt.Fprintf(&b, "\n- Project root: %s", p.ProjectRoot)
	}
	fmt.Fprintf(&b, "\n- Platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "\n- Date: %s", date.Format("2006-01-02"))
	if runtime.GOOS == "windows" {
		b.WriteString("\n- The bash tool runs Git Bash: use Unix commands. Windows paths like C:\\x\\y can be written /c/x/y.")
	}
	return b.String()
}

// File is a loaded context file.
type File struct {
	Path string
	Text string
}

// AgentsFiles returns ~/.rho/AGENTS.md followed by every AGENTS.md from the
// project root down to the working directory.
func AgentsFiles(p config.Paths) ([]File, error) {
	var files []File
	candidates := []string{filepath.Join(p.Home, "AGENTS.md")}
	var dirs []string
	for d := p.Cwd; ; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		if d == p.ProjectRoot || filepath.Dir(d) == d {
			break
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		candidates = append(candidates, filepath.Join(dirs[i], "AGENTS.md"))
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		if seen[c] {
			continue
		}
		seen[c] = true
		text, err := readOptional(c)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) != "" {
			files = append(files, File{Path: c, Text: text})
		}
	}
	return files, nil
}

func firstFile(paths ...string) (string, string, error) {
	for _, p := range paths {
		text, err := readOptional(p)
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(text) != "" {
			return p, text, nil
		}
	}
	return "", "", nil
}

func readOptional(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return "", nil
	}
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return "", nil // e.g. a directory named AGENTS.md
		}
		return "", err
	}
	return string(data), nil
}

// EstimateTokens is a rough token estimate (characters / 4).
func EstimateTokens(s string) int { return (len(s) + 3) / 4 }
