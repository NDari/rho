package app

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/session"
	"github.com/NDari/rho/core/theme"
)

//go:embed export.html
var exportPage string

// exportMaxResult caps each tool result in an export; the rest is cut.
const exportMaxResult = 20000

// markdown renders Markdown with GitHub's extensions, and code blocks
// highlighted with CSS classes the page's theme colors. Raw HTML in the
// text is left out, so an exported page cannot run what a model wrote.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM,
	highlighting.NewHighlighting(highlighting.WithFormatOptions(chromahtml.WithClasses(true)))))

var exportTmpl = template.Must(template.New("export").Funcs(template.FuncMap{
	"md": func(s string) template.HTML {
		var b bytes.Buffer
		if err := markdown.Convert([]byte(s), &b); err != nil {
			return template.HTML(template.HTMLEscapeString(s))
		}
		// Safe: goldmark escapes raw HTML unless told otherwise.
		return template.HTML(b.String())
	},
	"args":    exportArgs,
	"summary": exportSummary,
	"result": func(s string) string {
		if len(s) > exportMaxResult {
			return s[:exportMaxResult] + fmt.Sprintf("\n[... %d more characters not exported]", len(s)-exportMaxResult)
		}
		return s
	},
	"line": func(l events.DiffLine) string { return string(l.Kind) + l.Text },
	"kind": func(l events.DiffLine) string {
		switch l.Kind {
		case events.LineAdded:
			return "add"
		case events.LineRemoved:
			return "del"
		}
		return "ctx"
	},
}).Parse(exportPage))

type exportData struct {
	Title, Session, Cwd, Model, Date, Version string
	Items                                     []TranscriptItem
	CSS                                       template.CSS
}

// ExportHTML writes the conversation on the session's current path as one
// self-contained HTML page: messages, thinking, tool calls with their
// results and diffs, and compaction summaries. The page uses the first
// theme; given two, the first is for a reader whose system is light and
// the second for one whose system is dark. Without themes it uses the
// default light and dark themes.
func ExportHTML(w io.Writer, sess *session.Session, themes ...theme.Theme) error {
	css, err := exportCSS(themes)
	if err != nil {
		return err
	}
	h := sess.Header()
	cv := sess.BuildContext()
	d := exportData{
		Session: sess.ID(), Cwd: h.Cwd, Model: cv.Model, Version: h.RhoVersion,
		Items: SessionTranscript(sess).Items, CSS: css,
	}
	if !h.TS.IsZero() {
		d.Date = h.TS.Local().Format("2006-01-02 15:04")
	}
	d.Title = "Rho session " + sess.ID()
	for _, it := range d.Items {
		if it.Kind == ItemUser && strings.TrimSpace(it.Text) != "" {
			d.Title = firstLine(it.Text, 80)
			break
		}
	}
	return exportTmpl.Execute(w, d)
}

// ExportHTML writes the current session to path, or to rho-<id>.html in
// the working directory when path is empty, and returns the file written.
func (a *App) ExportHTML(path string) (string, error) {
	sess := a.Agent.Session()
	if path == "" {
		path = DefaultExportName(sess)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.Paths.Cwd, path)
	}
	cfg := a.Agent.Config()
	return path, WriteExport(path, sess, ExportThemes(cfg.Theme, cfg.ThemeDark, cfg.ThemeLight, a.Paths.Home)...)
}

// ExportThemes are the themes an export uses for the theme settings: the
// chosen theme, or for "system" the light and dark ones, so the page
// follows the reader's system.
func ExportThemes(name, dark, light, home string) []theme.Theme {
	set, _ := theme.Load(home)
	if name == "" || name == theme.System {
		l, _ := set.Resolve(theme.System, dark, light, false)
		d, _ := set.Resolve(theme.System, dark, light, true)
		return []theme.Theme{l, d}
	}
	t, _ := set.Resolve(name, dark, light, false)
	return []theme.Theme{t}
}

// exportCSS is the page's theme: its colors and its Chroma classes, the
// second theme inside a dark-mode media query.
func exportCSS(themes []theme.Theme) (template.CSS, error) {
	if len(themes) == 0 {
		set := theme.Builtin()
		l, _ := set.Get(theme.DefaultLight)
		d, _ := set.Get(theme.DefaultDark)
		themes = []theme.Theme{l, d}
	}
	var b strings.Builder
	formatter := chromahtml.New(chromahtml.WithClasses(true))
	for i, t := range themes[:min(2, len(themes))] {
		if i == 1 {
			b.WriteString("@media (prefers-color-scheme: dark) {\n")
		}
		b.WriteString(t.CSSRule(":root") + "\n")
		if err := formatter.WriteCSS(&b, t.ChromaStyle()); err != nil {
			return "", err
		}
		if i == 1 {
			b.WriteString("}\n")
		}
	}
	// Safe: built from theme colors and Chroma's own CSS, not page content.
	return template.CSS(b.String()), nil
}

// DefaultExportName is rho-<first 8 characters of the session id>.html.
func DefaultExportName(sess *session.Session) string {
	id := sess.ID()
	if len(id) > 8 {
		id = id[:8]
	}
	return "rho-" + id + ".html"
}

// WriteExport writes the HTML export of sess to path, in themes (see
// ExportHTML).
func WriteExport(path string, sess *session.Session, themes ...theme.Theme) error {
	var b bytes.Buffer
	if err := ExportHTML(&b, sess, themes...); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

// exportArgs pretty-prints tool arguments.
func exportArgs(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// exportSummary is the one-line description of a tool call: its path,
// command or pattern.
func exportSummary(tc *ToolCall) string {
	var m map[string]any
	_ = json.Unmarshal(tc.Args, &m)
	str := func(k string) string { s, _ := m[k].(string); return s }
	var s string
	switch tc.Name {
	case "bash":
		s = firstLine(str("command"), 120)
	case "grep", "find":
		s = str("pattern")
		if p := str("path"); p != "" {
			s += " in " + p
		}
	default:
		s = str("path")
	}
	return s
}
