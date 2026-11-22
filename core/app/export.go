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

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/session"
)

//go:embed export.html
var exportPage string

// exportMaxResult caps each tool result in an export; the rest is cut.
const exportMaxResult = 20000

// markdown renders Markdown with GitHub's extensions. Raw HTML in the
// text is left out, so an exported page cannot run what a model wrote.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

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
}

// ExportHTML writes the conversation on the session's current path as one
// self-contained HTML page: messages, thinking, tool calls with their
// results and diffs, and compaction summaries.
func ExportHTML(w io.Writer, sess *session.Session) error {
	h := sess.Header()
	cv := sess.BuildContext()
	d := exportData{
		Session: sess.ID(), Cwd: h.Cwd, Model: cv.Model, Version: h.RhoVersion,
		Items: SessionTranscript(sess).Items,
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
	return path, WriteExport(path, sess)
}

// DefaultExportName is rho-<first 8 characters of the session id>.html.
func DefaultExportName(sess *session.Session) string {
	id := sess.ID()
	if len(id) > 8 {
		id = id[:8]
	}
	return "rho-" + id + ".html"
}

// WriteExport writes the HTML export of sess to path.
func WriteExport(path string, sess *session.Session) error {
	var b bytes.Buffer
	if err := ExportHTML(&b, sess); err != nil {
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
