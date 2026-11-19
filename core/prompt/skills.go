package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/NDari/rho/core/config"
)

// Skill is an Agent Skills entry (a directory containing SKILL.md).
type Skill struct {
	Name        string
	Description string
	Path        string
}

// LoadSkills discovers skills in ~/.rho/skills and .rho/skills. Project
// skills override global ones with the same name. Problems are returned as
// warnings.
func LoadSkills(p config.Paths) ([]Skill, []string) {
	byName := map[string]Skill{}
	var warnings []string
	for _, dir := range []string{filepath.Join(p.Home, "skills"), filepath.Join(p.ProjectDir, "skills")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name(), "SKILL.md")
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			fm, _, err := Frontmatter(string(data))
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			name, _ := fm["name"].(string)
			desc, _ := fm["description"].(string)
			if name == "" {
				name = e.Name()
			}
			if desc == "" {
				warnings = append(warnings, fmt.Sprintf("%s: missing description; skipped", path))
				continue
			}
			byName[name] = Skill{Name: name, Description: strings.TrimSpace(desc), Path: path}
		}
	}
	out := make([]Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warnings
}

func skillsSection(skills []Skill) string {
	var b strings.Builder
	b.WriteString("# Skills\n\nSkills are instructions for specialized tasks. When a task matches a skill's description, read its file with the read tool before starting, and follow it.\n")
	for _, s := range skills {
		fmt.Fprintf(&b, "\n- %s: %s (%s)", s.Name, s.Description, s.Path)
	}
	return b.String()
}

// Frontmatter splits YAML frontmatter (between leading "---" lines) from a
// markdown body.
func Frontmatter(text string) (map[string]any, string, error) {
	text = strings.TrimPrefix(text, "\uFEFF")
	norm := strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(norm, "---\n") {
		return map[string]any{}, text, nil
	}
	rest := norm[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return map[string]any{}, text, nil
	}
	fm := map[string]any{}
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return nil, "", fmt.Errorf("invalid frontmatter: %w", err)
	}
	body := rest[end+4:]
	body = strings.TrimPrefix(body, "\n")
	return fm, body, nil
}
