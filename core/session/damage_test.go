package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDari/rho/core/provider"
)

// realisticSession writes a session shaped like a real one: a model
// change, user prompts, assistant text with thinking, tool calls with
// JSON arguments, tool results with display data, a label and a
// checkout. It returns the file path and the entries in file order.
func realisticSession(t *testing.T) (string, []Entry) {
	t.Helper()
	s := New(t.TempDir(), "/work/project", "test")
	var out []Entry
	add := func(e Entry) Entry {
		t.Helper()
		got, err := s.Append(e)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, got)
		return got
	}
	msg := func(m provider.Message) Entry { return add(Entry{Type: TypeMessage, Message: &m}) }
	add(Entry{Type: TypeModelChange, Provider: "local", Model: "qwen3-coder"})
	u := msg(provider.UserText("Why does `go test ./...` fail in core/session?\nIt says \"unexpected EOF\"."))
	msg(provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{
		{Type: provider.BlockThinking, Text: "Look at the test output first.", Provider: "local"},
		provider.Text("Let me run the tests."),
		{Type: provider.BlockToolCall, ID: "call_1", Name: "bash", Args: json.RawMessage(`{"command":"go test ./core/session/ 2>&1 | tail -20","timeout":60}`), Provider: "local"},
	}, Meta: provider.Meta{Provider: "local", Model: "qwen3-coder", StopReason: provider.StopToolUse, Usage: &provider.Usage{InputTokens: 5120, OutputTokens: 87}}})
	tr := provider.Message{Role: provider.RoleToolResult, Content: []provider.Block{provider.ToolResult("call_1", "--- FAIL: TestOpen (0.00s)\n    session_test.go:42: unexpected EOF\nFAIL\nexit status 1", true)}}
	add(Entry{Type: TypeMessage, Message: &tr, Display: json.RawMessage(`{"exit":1,"lines":4}`)})
	a := msg(provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.Text("The file ends in a partial line. Unicode check: naïve café, 日本語, emoji 🎉.")}})
	add(Entry{Type: TypeLabel, Target: a.ID, Label: "diagnosis"})
	add(Entry{Type: TypeCheckout, Target: u.ID})
	msg(provider.UserText("Try again, but only list the files."))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return s.Path(), out
}

func lineEnds(data []byte) []int {
	var ends []int
	for i, b := range data {
		if b == '\n' {
			ends = append(ends, i+1)
		}
	}
	return ends
}

// TestCrashAtEveryByte simulates a crash at every point of the last write
// (and at many points before it): the file must open, keep exactly the
// complete entries, accept new entries, and reopen with no problems.
func TestCrashAtEveryByte(t *testing.T) {
	path, entries := realisticSession(t)
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ends := lineEnds(full)
	headerEnd := ends[0]
	lastStart := ends[len(ends)-2]
	for cut := headerEnd; cut < len(full); cut++ {
		// Every byte of the last entry, and a sample of earlier ones.
		if cut < lastStart && cut%7 != 0 {
			continue
		}
		// An entry is complete once its JSON is, even without the newline.
		complete, torn := 0, false
		for _, e := range ends[1:] {
			if e-1 <= cut {
				complete++
			} else {
				torn = cut > ends[complete]
				break
			}
		}
		p := filepath.Join(t.TempDir(), "s.jsonl")
		if err := os.WriteFile(p, full[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Open(p)
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		if got := len(s.Entries()); got != complete {
			t.Fatalf("cut %d: %d entries, want %d", cut, got, complete)
		}
		if torn != (len(s.Problems()) == 1) {
			t.Fatalf("cut %d: problems = %q", cut, s.Problems())
		}
		if _, err := s.AppendMessage(provider.UserText("after the crash")); err != nil {
			t.Fatalf("cut %d: append: %v", cut, err)
		}
		_ = s.Close()
		r, err := Open(p)
		if err != nil {
			t.Fatalf("cut %d: reopen: %v", cut, err)
		}
		if probs := r.Problems(); len(probs) != 0 {
			t.Fatalf("cut %d: problems after append: %q", cut, probs)
		}
		got := r.Entries()
		if len(got) != complete+1 {
			t.Fatalf("cut %d: %d entries after append, want %d", cut, len(got), complete+1)
		}
		for i := range complete {
			if got[i].ID != entries[i].ID {
				t.Fatalf("cut %d: entry %d is %s, want %s", cut, i, got[i].ID, entries[i].ID)
			}
		}
		if last := got[complete]; last.Message.TextContent() != "after the crash" {
			t.Fatalf("cut %d: last entry %+v", cut, last)
		}
	}
}

func TestOpenDamagedFiles(t *testing.T) {
	path, entries := realisticSession(t)
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(full), "\n")
	lines = lines[:len(lines)-1] // trailing ""
	join := func(ls ...string) string { return strings.Join(ls, "") }
	torn := lines[3][:len(lines[3])/2] // half of the first assistant message

	tests := []struct {
		name     string
		data     string
		wantErr  string
		entries  int
		problems []string // substrings, in order
		context  int      // messages in BuildContext; -1 to skip
	}{
		{
			name:    "healthy",
			data:    join(lines...),
			entries: len(entries),
			context: 2,
		},
		{
			// Written by versions that appended after a torn line.
			name:     "partial entry joined to the next",
			data:     join(lines[0], lines[1], lines[2], torn+lines[3], join(lines[4:]...)),
			entries:  len(entries),
			problems: []string{"line 4: dropped a partial entry"},
			context:  2,
		},
		{
			name:     "unreadable line in the middle",
			data:     join(lines[0], lines[1], lines[2], "{not json\n", join(lines[4:]...)),
			entries:  len(entries) - 1,
			problems: []string{"line 4: skipped an unreadable entry", "missing earlier messages"},
			context:  -1,
		},
		{
			name:    "blank lines",
			data:    join(lines[0], "\n", lines[1], "  \n", join(lines[2:]...)),
			entries: len(entries),
			context: 2,
		},
		{
			name:    "unreadable header",
			data:    join(`{"id":"x","type":"hea`+"\n", join(lines[1:]...)),
			wantErr: ":1:",
		},
		{
			name:    "no header",
			data:    join(lines[1:]...),
			wantErr: "missing session header",
		},
		{
			name:    "empty file",
			data:    "",
			wantErr: "missing session header",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "s.jsonl")
			if err := os.WriteFile(p, []byte(tt.data), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := Open(p)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := len(s.Entries()); got != tt.entries {
				t.Errorf("entries = %d, want %d", got, tt.entries)
			}
			probs := s.Problems()
			if len(probs) != len(tt.problems) {
				t.Fatalf("problems = %q, want %q", probs, tt.problems)
			}
			for i, want := range tt.problems {
				if !strings.Contains(probs[i], want) {
					t.Errorf("problem %d = %q, want %q", i, probs[i], want)
				}
			}
			if tt.context >= 0 {
				if got := len(s.BuildContext().Messages); got != tt.context {
					t.Errorf("context messages = %d, want %d", got, tt.context)
				}
			}
			// Damage never stops the session from being continued.
			if _, err := s.AppendMessage(provider.UserText("continue")); err != nil {
				t.Fatal(err)
			}
			_ = s.Close()
			r, err := Open(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(r.Entries()); got != tt.entries+1 {
				t.Errorf("entries after append = %d, want %d", got, tt.entries+1)
			}
		})
	}
}

// TestTornLineChangedElsewhere keeps the file intact when it changed after
// Open: the next entry starts a new line instead of cutting anything.
func TestTornLineChangedElsewhere(t *testing.T) {
	path, entries := realisticSession(t)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"id":"abcd","type":"mess`)
	_ = f.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`age"`)
	_ = f.Close()
	if _, err := s.AppendMessage(provider.UserText("next")); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(r.Entries()); got != len(entries)+1 {
		t.Errorf("entries = %d, want %d", got, len(entries)+1)
	}
	if probs := r.Problems(); len(probs) != 1 || !strings.Contains(probs[0], "unreadable") {
		t.Errorf("problems = %q", probs)
	}
}

// TestWriteFailureRecovers checks that a failed write is not applied and
// that the session keeps working once writes succeed again.
func TestWriteFailureRecovers(t *testing.T) {
	path, entries := realisticSession(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// A read-only handle makes the next write fail, like a full disk.
	ro, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.file = ro
	leaf := s.Leaf()
	if _, err := s.AppendMessage(provider.UserText("lost")); err == nil || !strings.Contains(err.Error(), "writing session") {
		t.Fatalf("err = %v", err)
	}
	if s.Leaf() != leaf || len(s.Entries()) != len(entries) {
		t.Fatal("a failed write must not change the session")
	}
	if _, err := s.AppendMessage(provider.UserText("kept")); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems()) != 0 || len(r.Entries()) != len(entries)+1 {
		t.Fatalf("problems = %q, entries = %d", r.Problems(), len(r.Entries()))
	}
	cv := r.BuildContext()
	if last := cv.Messages[len(cv.Messages)-1].TextContent(); last != "kept" {
		t.Errorf("last message = %q", last)
	}
}

func TestPathToAndLabels(t *testing.T) {
	path, entries := realisticSession(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// entries: 0 model_change, 1 user, 2 assistant, 3 tool result,
	// 4 assistant, 5 label, 6 checkout, 7 user (branch from 1's position).
	got := s.PathTo(entries[4].ID)
	var ids []string
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	want := []string{entries[0].ID, entries[1].ID, entries[2].ID, entries[3].ID, entries[4].ID}
	if !equal(ids, want) {
		t.Errorf("PathTo = %v, want %v", ids, want)
	}
	if p := s.PathTo("nope"); len(p) != 0 {
		t.Errorf("PathTo unknown = %v", p)
	}
	if l := s.Labels(); l[entries[4].ID] != "diagnosis" || len(l) != 1 {
		t.Errorf("labels = %v", l)
	}
	if e, ok := s.Get(entries[7].ID); !ok || e.Parent != entries[1].ID {
		t.Errorf("branch entry = %+v, %v", e, ok)
	}
}
