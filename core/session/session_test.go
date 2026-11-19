package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NDari/rho/core/provider"
)

func appendText(t *testing.T, s *Session, role provider.Role, text string) Entry {
	t.Helper()
	e, err := s.AppendMessage(provider.Message{Role: role, Content: []provider.Block{provider.Text(text)}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func texts(c Context) []string {
	var out []string
	for _, m := range c.Messages {
		out = append(out, m.TextContent())
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPersistAndReopen(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, "/work", "test")
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatal("file should be created lazily")
	}
	appendText(t, s, provider.RoleUser, "hello")
	appendText(t, s, provider.RoleAssistant, "hi")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if r.ID() != s.ID() || r.Header().Cwd != "/work" {
		t.Errorf("header mismatch: %+v", r.Header())
	}
	if got := texts(r.BuildContext()); !equal(got, []string{"hello", "hi"}) {
		t.Errorf("context = %v", got)
	}
	appendText(t, r, provider.RoleUser, "more")
	_ = r.Close()
	r2, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r2.BuildContext().Messages); n != 3 {
		t.Errorf("messages after reopen = %d", n)
	}
}

func TestCheckoutBranches(t *testing.T) {
	s := New("", "/w", "test")
	u1 := appendText(t, s, provider.RoleUser, "u1")
	appendText(t, s, provider.RoleAssistant, "a1")
	appendText(t, s, provider.RoleUser, "u2")
	// Rewind to after u1's parent and branch.
	if err := s.Checkout(u1.Parent); err != nil {
		t.Fatal(err)
	}
	appendText(t, s, provider.RoleUser, "u1-alt")
	if got := texts(s.BuildContext()); !equal(got, []string{"u1-alt"}) {
		t.Errorf("branch context = %v", got)
	}
	if kids := s.Children(""); len(kids) != 2 {
		t.Errorf("root children = %d, want 2", len(kids))
	}
}

func TestCompactionContext(t *testing.T) {
	s := New("", "/w", "test")
	appendText(t, s, provider.RoleUser, "u1")
	appendText(t, s, provider.RoleAssistant, "a1")
	kept := appendText(t, s, provider.RoleUser, "u2")
	appendText(t, s, provider.RoleAssistant, "a2")
	if _, err := s.Append(Entry{Type: TypeCompaction, Summary: "S", FirstKept: kept.ID}); err != nil {
		t.Fatal(err)
	}
	appendText(t, s, provider.RoleUser, "u3")
	c := s.BuildContext()
	got := texts(c)
	if len(got) != 4 || got[1] != "u2" || got[3] != "u3" {
		t.Fatalf("context = %v", got)
	}
	if c.Messages[0].Meta.Synthetic != SyntheticCompaction || c.EntryIDs[0] != "" {
		t.Errorf("first message should be the synthetic summary: %+v", c.Messages[0])
	}
}

func TestAsideContext(t *testing.T) {
	s := New("", "/w", "test")
	appendText(t, s, provider.RoleUser, "main")
	mainLeaf := appendText(t, s, provider.RoleAssistant, "answer")
	if _, err := s.Append(Entry{Type: TypeAsideStart}); err != nil {
		t.Fatal(err)
	}
	appendText(t, s, provider.RoleUser, "side q")
	c := s.BuildContext()
	if !c.Aside || c.MainLeaf != mainLeaf.ID || len(c.Messages) != 3 {
		t.Fatalf("aside context = %+v", c)
	}
	if err := s.Checkout(c.MainLeaf); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(Entry{Type: TypeAsideSummary, Note: "remember X"}); err != nil {
		t.Fatal(err)
	}
	c = s.BuildContext()
	if c.Aside || len(c.Messages) != 3 || c.Messages[2].Meta.Synthetic != SyntheticAsideNote {
		t.Errorf("main context after aside = %v", texts(c))
	}
}

func TestListAndLatest(t *testing.T) {
	dir := t.TempDir()
	s1 := New(dir, "/w", "test")
	appendText(t, s1, provider.RoleUser, "first session")
	_ = s1.Close()
	s2 := New(dir, "/w", "test")
	appendText(t, s2, provider.RoleUser, "second session")
	if _, err := s2.Append(Entry{Type: TypeModelChange, Provider: "p", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()
	// Make s2 clearly newer.
	future := s1.Header().TS.Add(10e9)
	_ = os.Chtimes(s2.Path(), future, future)

	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Title != "second session" || list[0].Model != "m" {
		t.Fatalf("list = %+v", list)
	}
	latest, _ := Latest(dir)
	if latest != s2.Path() {
		t.Errorf("latest = %s", latest)
	}
}

func TestOpenToleratesTornLastLine(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, "/w", "test")
	appendText(t, s, provider.RoleUser, "ok")
	_ = s.Close()
	f, err := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"id":"zz","type":"mess`)
	_ = f.Close()
	r, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.BuildContext().Messages) != 1 {
		t.Error("expected the valid message to load")
	}
}

func TestEncodeCwd(t *testing.T) {
	if got := EncodeCwd(filepath.FromSlash("/home/u/proj")); got != "--home-u-proj--" {
		t.Errorf("EncodeCwd = %q", got)
	}
}
