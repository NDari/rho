package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/NDari/rho/core/config"
)

// Template is a prompt template that becomes a slash command.
type Template struct {
	Name        string
	Description string
	Path        string
	Body        string
}

// LoadTemplates discovers *.md files in ~/.rho/prompts and .rho/prompts.
// Project templates override global ones with the same name.
func LoadTemplates(p config.Paths) ([]Template, []string) {
	byName := map[string]Template{}
	var warnings []string
	for _, dir := range []string{filepath.Join(p.Home, "prompts"), p.Project("prompts")} {
		entries, err := os.ReadDir(dir)
		if dir == "" || err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			fm, body, err := Frontmatter(string(data))
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			desc, _ := fm["description"].(string)
			if desc == "" {
				desc = firstLine(body)
			}
			byName[name] = Template{Name: name, Description: desc, Path: path, Body: body}
		}
	}
	out := make([]Template, 0, len(byName))
	for _, t := range byName {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warnings
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(l, "# "))
		if l != "" {
			if len(l) > 80 {
				l = l[:80] + "…"
			}
			return l
		}
	}
	return ""
}

var argRE = regexp.MustCompile(`\$(ARGS|\d+)|\$\{(ARGS|\d+)\}`)

// Expand substitutes $ARGS (all arguments) and $1..$N (positional,
// whitespace-separated, quotes respected).
func (t Template) Expand(args string) string {
	args = strings.TrimSpace(args)
	pos := SplitArgs(args)
	used := false
	out := argRE.ReplaceAllStringFunc(t.Body, func(m string) string {
		used = true
		name := strings.Trim(m, "${}")
		if name == "ARGS" {
			return args
		}
		n, _ := strconv.Atoi(name)
		if n >= 1 && n <= len(pos) {
			return pos[n-1]
		}
		return ""
	})
	if !used && args != "" {
		out = strings.TrimRight(out, "\n") + "\n\n" + args
	}
	return out
}

// SplitArgs splits on whitespace, honoring single and double quotes.
func SplitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	inArg := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			inArg = true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				out = append(out, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if inArg {
		out = append(out, cur.String())
	}
	return out
}
