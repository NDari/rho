package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// completion is the popup shown while typing /commands or @files.
type completion struct {
	kind   string // "command" or "file"
	items  []pickerItem
	cursor int
	token  string // the text being completed (including / or @)
}

// commandInfo describes a slash command for completion and /help.
type commandInfo struct {
	name string
	desc string
}

// computeCompletion inspects the text before the cursor.
func computeCompletion(before string, isFirstLine bool, cmds []commandInfo, cwd string) *completion {
	if isFirstLine && strings.HasPrefix(before, "/") && !strings.ContainsAny(before, " \t\n") {
		prefix := strings.ToLower(before[1:])
		var items []pickerItem
		for _, c := range cmds {
			if strings.HasPrefix(strings.ToLower(c.name), prefix) {
				items = append(items, pickerItem{title: "/" + c.name, detail: c.desc, value: "/" + c.name + " "})
			}
		}
		if len(items) == 0 {
			return nil
		}
		return &completion{kind: "command", items: items, token: before}
	}
	tok := before
	if i := strings.LastIndexAny(before, " \t\n"); i >= 0 {
		tok = before[i+1:]
	}
	if !strings.HasPrefix(tok, "@") {
		return nil
	}
	items := fileCandidates(cwd, tok[1:])
	if len(items) == 0 {
		return nil
	}
	return &completion{kind: "file", items: items, token: tok}
}

func fileCandidates(cwd, partial string) []pickerItem {
	dirPart, prefix := "", partial
	if i := strings.LastIndex(partial, "/"); i >= 0 {
		dirPart, prefix = partial[:i+1], partial[i+1:]
	}
	dir := dirPart
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dirPart)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var dirs, files []pickerItem
	lp := strings.ToLower(prefix)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(name), lp) {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, pickerItem{title: dirPart + name + "/", value: "@" + dirPart + name + "/"})
		} else {
			files = append(files, pickerItem{title: dirPart + name, value: "@" + dirPart + name + " "})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].title < dirs[j].title })
	sort.Slice(files, func(i, j int) bool { return files[i].title < files[j].title })
	out := append(dirs, files...)
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

func (c *completion) move(d int) {
	c.cursor = (c.cursor + d + len(c.items)) % len(c.items)
}

func (c *completion) render(rc *renderCtx, maxRows int) []string {
	start := 0
	if c.cursor >= maxRows {
		start = c.cursor - maxRows + 1
	}
	var out []string
	for i := start; i < len(c.items) && i < start+maxRows; i++ {
		it := c.items[i]
		line := it.title
		if it.detail != "" {
			line += "  " + rc.st.muted.Render(it.detail)
		}
		line = ansi.Truncate(line, rc.width-4, "…")
		if i == c.cursor {
			out = append(out, rc.st.accent.Render("▸ ")+rc.st.selected.Render(ansi.Truncate(it.title, rc.width-4, "…"))+
				ansi.Truncate(strings.TrimPrefix(line, it.title), max(0, rc.width-4-ansi.StringWidth(it.title)), "…"))
		} else {
			out = append(out, "  "+line)
		}
	}
	return out
}
