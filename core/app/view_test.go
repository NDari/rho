package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/session"
)

func newTestApp(t *testing.T) (*App, *session.Session) {
	t.Helper()
	t.Setenv("RHO_HOME", t.TempDir())
	a, err := New(Options{Cwd: t.TempDir(), Model: "openai/test-model", NoExtensions: true})
	if err != nil {
		t.Fatal(err)
	}
	s := a.Agent.Session()
	t.Cleanup(func() { _ = s.Close() }) // Windows cannot remove open files
	return a, s
}

func appendMsg(t *testing.T, s *session.Session, m provider.Message) string {
	t.Helper()
	e, err := s.AppendMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	return e.ID
}

func TestTranscript(t *testing.T) {
	a, s := newTestApp(t)
	user := provider.UserText("look at @a.go")
	user.Content = append(user.Content, provider.Text("<file path=\"a.go\">\npackage a\n</file>"))
	appendMsg(t, s, user)
	appendMsg(t, s, provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{
		{Type: provider.BlockThinking, Text: "hmm"},
		{Type: provider.BlockToolCall, ID: "c1", Name: "edit", Args: json.RawMessage(`{"path":"a.go"}`)},
	}})
	disp, _ := json.Marshal(events.Display{Kind: "diff", Path: "a.go", Diff: &events.Diff{Added: 1}})
	result := provider.Message{Role: provider.RoleToolResult, Content: []provider.Block{provider.ToolResult("c1", "ok", false)}}
	if _, err := s.Append(session.Entry{Type: session.TypeMessage, Message: &result, Display: disp}); err != nil {
		t.Fatal(err)
	}
	// An assistant message with only a tool call shows just the tool.
	appendMsg(t, s, provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{
		{Type: provider.BlockToolCall, ID: "c2", Name: "bash"},
	}})
	appendMsg(t, s, provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.Text("done")}})

	tr := a.Transcript()
	var kinds []string
	for _, it := range tr.Items {
		kinds = append(kinds, it.Kind)
	}
	want := []string{ItemUser, ItemAssistant, ItemTool, ItemTool, ItemAssistant}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	if u := tr.Items[0]; u.Text != "look at @a.go" || !reflect.DeepEqual(u.Attached, []string{"a.go"}) {
		t.Errorf("user item = %+v", u)
	}
	if tr.Items[1].Thinking != "hmm" {
		t.Errorf("thinking = %q", tr.Items[1].Thinking)
	}
	tc := tr.Items[2].Tool
	if tc.Name != "edit" || tc.Result != "ok" || !tc.Done || tc.Display == nil || tc.Display.Diff.Added != 1 {
		t.Errorf("tool = %+v", tc)
	}
	if c2 := tr.Items[3].Tool; c2.Done {
		t.Errorf("unanswered tool call is done: %+v", c2)
	}
	if tr.Aside {
		t.Error("aside set on the main branch")
	}
}

func TestPathMessagesAndTree(t *testing.T) {
	a, s := newTestApp(t)
	u1 := appendMsg(t, s, provider.UserText("first"))
	a1 := appendMsg(t, s, provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.Text("answer one")}})
	if err := a.Agent.Label(a1, "good"); err != nil {
		t.Fatal(err)
	}
	// Branch: go back to u1 and answer differently.
	if err := s.Checkout(u1); err != nil {
		t.Fatal(err)
	}
	a2 := appendMsg(t, s, provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{
		{Type: provider.BlockToolCall, ID: "c1", Name: "read"},
	}})

	msgs := a.PathMessages(provider.RoleUser, provider.RoleAssistant)
	if len(msgs) != 2 || msgs[0].ID != a2 || msgs[0].ToolName != "read" || msgs[1].ID != u1 {
		t.Fatalf("path messages = %+v", msgs)
	}
	if got := a.PathMessages(provider.RoleUser); len(got) != 1 || got[0].Text != "first" {
		t.Errorf("user messages = %+v", got)
	}

	tree := a.Tree()
	// a2 has only a tool call, so it is left out; a1 is on another branch.
	if len(tree) != 2 {
		t.Fatalf("tree = %+v", tree)
	}
	if n := tree[0]; n.ID != u1 || !n.OnPath || n.Current || n.Depth != 0 {
		t.Errorf("root node = %+v", n)
	}
	if n := tree[1]; n.ID != a1 || n.OnPath || n.Label != "good" || n.Depth != 1 {
		t.Errorf("branch node = %+v", n)
	}
}

func TestCopyText(t *testing.T) {
	a, s := newTestApp(t)
	if got := a.CopyText(false); got != "" {
		t.Errorf("empty session copy = %q", got)
	}
	appendMsg(t, s, provider.UserText("q"))
	appendMsg(t, s, provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.Text("a1")}})
	appendMsg(t, s, provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.Text("a2")}})
	tests := []struct {
		all  bool
		want string
	}{
		{false, "a2"},
		{true, "## user\n\nq\n\n## assistant\n\na1\n\n## assistant\n\na2\n\n"},
	}
	for _, tc := range tests {
		if got := a.CopyText(tc.all); got != tc.want {
			t.Errorf("CopyText(%v) = %q, want %q", tc.all, got, tc.want)
		}
	}
}

func TestBuildUserMessage(t *testing.T) {
	a, _ := newTestApp(t)
	if err := os.WriteFile(filepath.Join(a.Paths.Cwd, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := a.BuildUserMessage("see @a.txt, and @a.txt and @missing.txt")
	if len(msg.Content) != 2 {
		t.Fatalf("content = %+v", msg.Content)
	}
	if got := msg.Content[1].Text; !strings.HasPrefix(got, `<file path="a.txt">`) || !strings.Contains(got, "hello") {
		t.Errorf("attachment = %q", got)
	}
	text, attached := UserMessageText(msg)
	if text != "see @a.txt, and @a.txt and @missing.txt" || !reflect.DeepEqual(attached, []string{"a.txt"}) {
		t.Errorf("UserMessageText = %q, %v", text, attached)
	}
}

func TestFileCompletions(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"src", "scripts", ".git"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"README.md", "go.mod", ".env", "src/main.go", "src/Makefile"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		partial string
		want    []string
	}{
		{"", []string{"scripts/", "src/", "README.md", "go.mod"}},
		{"s", []string{"scripts/", "src/"}},
		{"r", []string{"README.md"}},
		{".", []string{".git/", ".env"}},
		{"src/", []string{"src/Makefile", "src/main.go"}},
		{"src/ma", []string{"src/Makefile", "src/main.go"}},
		{"missing/", nil},
		{filepath.Join(dir, "src") + "/main", []string{filepath.Join(dir, "src") + "/main.go"}},
	}
	for _, tt := range tests {
		if got := FileCompletions(dir, tt.partial); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("FileCompletions(%q) = %q, want %q", tt.partial, got, tt.want)
		}
	}
}
