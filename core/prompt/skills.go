package prompt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/docs"
)

// Skill is an Agent Skills entry (a directory containing SKILL.md).
type Skill struct {
	Name        string
	Description string
	Path        string
}

// LoadSkills returns Rho's built-in skills and the skills in ~/.rho/skills
// and .rho/skills. Global skills override built-in ones with the same name,
// and project skills override both. Problems are returned as warnings.
func LoadSkills(p config.Paths) ([]Skill, []string) {
	byName := map[string]Skill{}
	var warnings []string
	if dir, err := docs.Install(p.Home); err != nil {
		warnings = append(warnings, err.Error())
	} else {
		for name, path := range docs.Skills(dir) {
			s, err := readSkill(path, name)
			switch {
			case errors.Is(err, errNoSkill):
			case err != nil:
				warnings = append(warnings, err.Error())
			default:
				byName[s.Name] = s
			}
		}
	}
	for _, dir := range []string{filepath.Join(p.Home, "skills"), p.Project("skills")} {
		entries, err := os.ReadDir(dir)
		if dir == "" || err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			s, err := readSkill(filepath.Join(dir, e.Name(), "SKILL.md"), e.Name())
			switch {
			case errors.Is(err, errNoSkill):
			case err != nil:
				warnings = append(warnings, err.Error())
			default:
				byName[s.Name] = s
			}
		}
	}
	out := make([]Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warnings
}

// errNoSkill means there is no readable SKILL.md, which is not a problem.
var errNoSkill = errors.New("no skill")

// readSkill parses a SKILL.md file; fallback names a skill without a name
// in its frontmatter.
func readSkill(path, fallback string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, errNoSkill
	}
	fm, _, err := Frontmatter(string(data))
	if err != nil {
		return Skill{}, fmt.Errorf("%s: %w", path, err)
	}
	name, _ := fm["name"].(string)
	desc, _ := fm["description"].(string)
	if name == "" {
		name = fallback
	}
	if desc == "" {
		return Skill{}, fmt.Errorf("%s: missing description; skipped", path)
	}
	return Skill{Name: name, Description: strings.TrimSpace(desc), Path: path}, nil
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
