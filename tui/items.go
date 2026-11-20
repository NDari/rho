package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/NDari/rho/core/events"
)

// renderCtx carries what items need to render themselves.
type renderCtx struct {
	width        int
	theme        Theme
	st           styles
	md           *markdown
	hl           *highlighter
	diff         *diffRenderer
	expandAll    bool
	showThinking bool
	sideBySide   int
	spinner      string
	cwd          string
	key          string // changes whenever any of the above changes
}

// item is one block of the transcript.
type item interface {
	render(rc *renderCtx) []string
}

// cached memoizes an item's rendering until it changes.
type cached struct {
	version   int
	key       string
	renderedV int
	lines     []string
}

func (c *cached) touch() { c.version++ }

func (c *cached) get(rc *renderCtx, live bool, f func() []string) []string {
	if !live && c.lines != nil && c.key == rc.key && c.renderedV == c.version {
		return c.lines
	}
	c.lines = f()
	c.key = rc.key
	c.renderedV = c.version
	return c.lines
}

func stripANSI(s string) string { return ansi.Strip(s) }

func wrap(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	return strings.Split(ansi.Wrap(s, width, ""), "\n")
}

func indent(lines []string, prefix string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = prefix + l
	}
	return out
}

// --- user message ---

type userItem struct {
	cached
	text string
}

func (u *userItem) render(rc *renderCtx) []string {
	return u.get(rc, false, func() []string {
		bar := lipgloss.NewStyle().Foreground(c(rc.theme.Accent)).Background(c(rc.theme.UserBg)).Render("▌ ")
		var out []string
		for _, l := range wrap(expandTabs(u.text), rc.width-3) {
			pad := max(0, rc.width-2-ansi.StringWidth(l))
			out = append(out, bar+rc.st.user.Render(l+strings.Repeat(" ", pad)))
		}
		return out
	})
}

// --- assistant message ---

type assistantItem struct {
	cached
	text      string
	thinking  string
	streaming bool
	composing string
	cancelled bool
	sm        streamingMarkdown
	// requested is when the request was sent; thinkStart and thinkEnd
	// bracket the thinking. They are zero in a transcript loaded from the
	// session, which does not keep them.
	requested  time.Time
	thinkStart time.Time
	thinkEnd   time.Time
}

func (a *assistantItem) empty() bool {
	return a.text == "" && a.thinking == "" && a.composing == ""
}

// startThinking records thinking output at now. Thinking that resumes
// after text continues the same span.
func (a *assistantItem) startThinking(now time.Time) {
	if a.thinkStart.IsZero() {
		a.thinkStart = now
	}
	a.thinkEnd = time.Time{}
}

// endThinking records that the thinking ended at now, if it was going.
func (a *assistantItem) endThinking(now time.Time) {
	if !a.thinkStart.IsZero() && a.thinkEnd.IsZero() {
		a.thinkEnd = now
	}
}

// thinkingLabel is "∴ Thinking 14s ⠋" while the model thinks and
// "∴ Thought for 15s · 14:10:22" (when it finished) afterwards, or "" when
// the times are not known.
func (a *assistantItem) thinkingLabel(rc *renderCtx) string {
	switch {
	case a.thinkStart.IsZero():
		return ""
	case a.thinkEnd.IsZero() && a.streaming:
		return fmt.Sprintf("∴ Thinking %s %s", elapsed(time.Since(a.thinkStart)), rc.spinner)
	case a.thinkEnd.IsZero():
		return "∴ Thought"
	default:
		return fmt.Sprintf("∴ Thought for %s · %s", elapsed(a.thinkEnd.Sub(a.thinkStart)), a.thinkEnd.Format("15:04:05"))
	}
}

func (a *assistantItem) render(rc *renderCtx) []string {
	return a.get(rc, a.streaming, func() []string {
		var out []string
		if a.streaming && a.empty() && !a.requested.IsZero() {
			// Nothing has arrived yet: the model is reading the prompt.
			return []string{rc.st.muted.Render(fmt.Sprintf("%s waiting for the model · %s", rc.spinner, elapsed(time.Since(a.requested))))}
		}
		if a.thinking != "" {
			label := a.thinkingLabel(rc)
			if rc.showThinking {
				if label != "" {
					out = append(out, rc.st.thinking.Render(label))
				}
				for _, l := range wrap(strings.TrimSpace(a.thinking), rc.width-4) {
					out = append(out, rc.st.thinking.Render("┊ "+l))
				}
			} else {
				if label == "" {
					label = "∴ Thought"
				}
				n := len(strings.Split(strings.TrimSpace(a.thinking), "\n"))
				out = append(out, rc.st.thinking.Render(fmt.Sprintf("%s · %s · ctrl+t to show", label, plural(n, "line"))))
			}
		}
		if strings.TrimSpace(a.text) != "" {
			if len(out) > 0 {
				out = append(out, "")
			}
			md := a.sm.render(rc.md, a.text, rc.width-2, !a.streaming)
			out = append(out, md...)
		}
		if a.composing != "" && a.streaming {
			out = append(out, rc.st.muted.Render(fmt.Sprintf("%s preparing %s…", rc.spinner, a.composing)))
		}
		if a.cancelled {
			out = append(out, rc.st.warning.Render("⏹ cancelled"))
		}
		return out
	})
}

// --- tool call ---

type toolItem struct {
	cached
	id       string
	name     string
	args     json.RawMessage
	running  bool
	isError  bool
	output   strings.Builder // live output while running
	result   string          // model-facing result text
	display  *events.Display
	duration time.Duration
	started  time.Time
}

func (t *toolItem) argMap() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(t.args, &m)
	return m
}

func (t *toolItem) summary(cwd string) string {
	m := t.argMap()
	str := func(k string) string {
		if v, ok := m[k].(string); ok {
			if k == "path" {
				return relPath(cwd, v)
			}
			return v
		}
		return ""
	}
	switch t.name {
	case "read":
		s := str("path")
		if off, ok := m["offset"].(float64); ok && off > 0 {
			s += fmt.Sprintf(":%d", int(off))
		}
		return s
	case "write", "edit":
		return str("path")
	case "bash":
		cmd := str("command")
		if i := strings.IndexByte(cmd, '\n'); i >= 0 {
			cmd = cmd[:i] + " …"
		}
		return cmd
	}
	if len(t.args) == 0 {
		return ""
	}
	var compact strings.Builder
	if b, err := json.Marshal(m); err == nil {
		compact.Write(b)
	}
	return compact.String()
}

func (t *toolItem) render(rc *renderCtx) []string {
	return t.get(rc, t.running, func() []string {
		var icon string
		switch {
		case t.running:
			icon = rc.st.accent.Render(rc.spinner)
		case t.isError:
			icon = rc.st.errorS.Render("✗")
		default:
			icon = rc.st.success.Render("✓")
		}
		right := ""
		switch {
		case t.running:
			right = fmt.Sprintf("%.0fs", time.Since(t.started).Seconds())
		case t.duration > 0:
			right = formatDuration(t.duration)
		}
		if stat := t.stat(); stat != "" {
			if right != "" {
				right = stat + " · " + right
			} else {
				right = stat
			}
		}
		name := rc.st.toolName.Render(t.name)
		rightW := ansi.StringWidth(right)
		avail := rc.width - 4 - ansi.StringWidth(t.name) - rightW - 2
		sum := ansi.Truncate(t.summary(rc.cwd), max(avail, 5), "…")
		gap := max(1, rc.width-3-ansi.StringWidth(t.name)-1-ansi.StringWidth(sum)-rightW)
		header := icon + " " + name + " " + rc.st.text.Render(sum) + strings.Repeat(" ", gap) + rc.st.muted.Render(right)
		out := []string{header}
		body := t.body(rc)
		for i, l := range body {
			prefix := "  │ "
			if i == len(body)-1 {
				prefix = "  └ "
			}
			out = append(out, rc.st.subtle.Render(prefix)+l)
		}
		return out
	})
}

func (t *toolItem) stat() string {
	d := t.display
	if d == nil {
		return ""
	}
	switch {
	case d.Diff != nil:
		return diffStat(d.Diff)
	case t.name == "bash" && d.ExitCode != nil && *d.ExitCode > 0:
		return fmt.Sprintf("exit %d", *d.ExitCode)
	case t.name == "read":
		return d.Summary
	}
	return ""
}

// elapsed formats a time to the nearest second: "14s", "2m5s".
func elapsed(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return d.String()
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

func lastLines(s string, n int) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (t *toolItem) body(rc *renderCtx) []string {
	w := rc.width - 4
	trunc := func(lines []string) []string {
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = ansi.Truncate(expandTabs(l), w, "…")
		}
		return out
	}
	if t.running {
		if t.output.Len() == 0 {
			return nil
		}
		return trunc(styleLines(rc.st.muted, lastLines(t.output.String(), 8)))
	}
	d := t.display
	if t.isError && (d == nil || d.Diff == nil) {
		text := t.result
		if d != nil && d.Text != "" && t.name == "bash" {
			text = d.Text + "\n" + t.result[strings.LastIndex(t.result, "\n")+1:]
		}
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if !rc.expandAll && len(lines) > 6 {
			lines = append([]string{fmt.Sprintf("… %d lines hidden (ctrl+o)", len(lines)-6)}, lines[len(lines)-6:]...)
		}
		return trunc(styleLines(rc.st.errorS, lines))
	}
	if d == nil {
		lines := strings.Split(strings.TrimRight(t.result, "\n"), "\n")
		if !rc.expandAll {
			if len(lines) > 1 {
				return trunc(styleLines(rc.st.muted, []string{lines[0] + fmt.Sprintf("  (+%d lines)", len(lines)-1)}))
			}
		}
		return trunc(styleLines(rc.st.muted, lines))
	}
	switch {
	case d.Diff != nil:
		maxRows := 40
		if rc.expandAll {
			maxRows = 0
		}
		side := rc.sideBySide > 0 && rc.width >= rc.sideBySide
		return rc.diff.Render(d.Diff, d.Path, w, side, maxRows)
	case t.name == "bash":
		text := strings.TrimRight(d.Text, "\n")
		if text == "" {
			return []string{rc.st.muted.Render("(no output)")}
		}
		lines := strings.Split(text, "\n")
		if !rc.expandAll {
			if len(lines) <= 3 {
				return trunc(styleLines(rc.st.muted, lines))
			}
			return trunc(append(
				styleLines(rc.st.muted, lines[len(lines)-3:]),
				rc.st.subtle.Render(fmt.Sprintf("%d lines · ctrl+o to expand", len(lines)))))
		}
		if len(lines) > 2000 {
			lines = lines[len(lines)-2000:]
		}
		out := trunc(styleLines(rc.st.muted, lines))
		if d.FullOutputPath != "" {
			out = append(out, rc.st.subtle.Render("full output: "+d.FullOutputPath))
		}
		return out
	case d.Kind == "text" && d.Text != "":
		if !rc.expandAll {
			return nil
		}
		lines := strings.Split(strings.TrimRight(d.Text, "\n"), "\n")
		hl := rc.hl.Highlight(d.Path, lines)
		return trunc(hl)
	}
	return nil
}

func styleLines(st lipgloss.Style, lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = st.Render(stripANSI(expandTabs(l)))
	}
	return out
}

// --- notices, errors, banners ---

type noticeItem struct {
	cached
	level string // info, warn, error, banner
	text  string
}

func (n *noticeItem) render(rc *renderCtx) []string {
	return n.get(rc, false, func() []string {
		st, icon := rc.st.muted, "·"
		switch n.level {
		case "warn":
			st, icon = rc.st.warning, "!"
		case "error":
			st, icon = rc.st.errorS, "✗"
		case "banner":
			box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(rc.theme.Warning)).
				Foreground(c(rc.theme.Warning)).Padding(0, 1).Width(min(rc.width, 80))
			return strings.Split(box.Render(n.text), "\n")
		}
		var out []string
		for i, l := range wrap(n.text, rc.width-3) {
			p := "  "
			if i == 0 {
				p = icon + " "
			}
			out = append(out, st.Render(p+l))
		}
		return out
	})
}

// --- compaction ---

type compactionItem struct {
	cached
	running bool
	reason  string
	before  int
	after   int
	err     string
	summary string
}

func (ci *compactionItem) render(rc *renderCtx) []string {
	return ci.get(rc, ci.running, func() []string {
		switch {
		case ci.running:
			return []string{rc.st.accent.Render(fmt.Sprintf("%s compacting context (%s tokens, %s)…", rc.spinner, humanTokens(ci.before), ci.reason))}
		case ci.err != "":
			return []string{rc.st.warning.Render("! compaction failed: " + ci.err)}
		}
		head := rc.st.accent.Render("⇣ context compacted")
		if ci.before > 0 {
			head += rc.st.muted.Render(fmt.Sprintf("  %s → %s tokens", humanTokens(ci.before), humanTokens(ci.after)))
		}
		out := []string{head}
		if ci.summary != "" && rc.expandAll {
			out = append(out, indent(rc.md.Render(ci.summary, rc.width-4), "  ")...)
		} else if ci.summary != "" {
			out = append(out, rc.st.subtle.Render("  summary hidden · ctrl+o to show"))
		}
		return out
	})
}

func humanTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// --- static text block (help, context report) ---

type textItem struct {
	cached
	title string
	body  string
	md    bool
}

func (t *textItem) render(rc *renderCtx) []string {
	return t.get(rc, false, func() []string {
		var out []string
		if t.title != "" {
			out = append(out, rc.st.title.Render(t.title))
		}
		if t.md {
			out = append(out, rc.md.Render(t.body, rc.width-2)...)
		} else {
			for _, l := range strings.Split(t.body, "\n") {
				out = append(out, wrap(l, rc.width-2)...)
			}
		}
		return out
	})
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func relPath(cwd, p string) string {
	if cwd != "" && strings.HasPrefix(p, cwd+string(filepath.Separator)) {
		return p[len(cwd)+1:]
	}
	return shortenHome(p)
}
