package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/session"
)

func TestExportHTML(t *testing.T) {
	dir := t.TempDir()
	sess := session.New(dir, dir, "test")
	t.Cleanup(func() { _ = sess.Close() })
	add := func(m provider.Message, display *events.Display) {
		t.Helper()
		e := session.Entry{Type: session.TypeMessage, Message: &m}
		if display != nil {
			e.Display, _ = json.Marshal(display)
		}
		if _, err := sess.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	add(provider.UserText("Fix **the bug** <script>alert(1)</script>"), nil)
	add(provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{
		{Type: provider.BlockThinking, Text: "look at main.go"},
		provider.Text("Done. See `main.go`:\n\n| a | b |\n|---|---|\n| 1 | 2 |"),
		{Type: provider.BlockToolCall, ID: "c1", Name: "edit", Args: json.RawMessage(`{"path":"main.go"}`)},
	}}, nil)
	add(provider.Message{Role: provider.RoleToolResult, Content: []provider.Block{
		{Type: provider.BlockToolResult, CallID: "c1", Content: []provider.Block{provider.Text("edited")}},
	}}, &events.Display{Kind: "diff", Diff: &events.Diff{Hunks: []events.Hunk{{Lines: []events.DiffLine{
		{Kind: events.LineRemoved, Text: "old()"}, {Kind: events.LineAdded, Text: "new()"},
	}}}}})

	var b bytes.Buffer
	if err := ExportHTML(&b, sess); err != nil {
		t.Fatal(err)
	}
	page := b.String()
	for _, want := range []string{
		"<title>Fix **the bug** &lt;script&gt;alert(1)&lt;/script&gt;</title>",
		"<strong>the bug</strong>",
		"<table>",
		"<code>main.go</code>",
		`<span class="name">edit</span> main.go`,
		`<span class="del">-old()</span>`,
		`<span class="add">&#43;new()</span>`,
		"look at main.go",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, "<script>") {
		t.Error("raw HTML from the conversation reached the page")
	}
}
