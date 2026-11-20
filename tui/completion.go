package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/NDari/rho/core/app"
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
	var out []pickerItem
	for _, p := range app.FileCompletions(cwd, partial) {
		v := "@" + p
		if !strings.HasSuffix(p, "/") {
			v += " "
		}
		out = append(out, pickerItem{title: p, value: v})
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
