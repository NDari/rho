package xmltools

import (
	"regexp"
	"strings"
	"testing"
)

type splitCall struct {
	name, body string
	complete   bool
}

type splitResult struct {
	text   string
	calls  []splitCall
	halted bool
	order  []string
}

var fenceLeak = regexp.MustCompile("```tool_call[\\s>]")

// runSplit feeds text in pseudo-random chunks of 1 to 7 bytes and checks
// the invariants that must hold for any chunking.
func runSplit(t *testing.T, text string, seed int) splitResult {
	t.Helper()
	s := seed
	rnd := func() int {
		s = (s*1103515245 + 12345) & 0x7fffffff
		return s
	}
	sp := &splitter{}
	var events []splitEvent
	for i := 0; i < len(text); {
		n := 1 + rnd()%7
		end := min(i+n, len(text))
		events = append(events, sp.push(text[i:end])...)
		i = end
	}
	events = append(events, sp.finish()...)
	var r splitResult
	body := ""
	for _, e := range events {
		switch e.kind {
		case splitText:
			r.text += e.text
			if len(r.order) == 0 || r.order[len(r.order)-1] != "text" {
				r.order = append(r.order, "text")
			}
			if strings.Contains(e.text, "<tool_call ") || strings.Contains(e.text, "<tool_call>") || fenceLeak.MatchString(e.text) {
				t.Fatalf("seed %d: a tag leaked into text: %q", seed, e.text)
			}
		case splitToolStart:
			body = ""
		case splitToolDelta:
			body += e.text
			if e.body != body {
				t.Fatalf("seed %d: body %q != deltas %q", seed, e.body, body)
			}
			if strings.Contains(e.body, "</tool_call") {
				t.Fatalf("seed %d: body holds a fragment of the close tag: %q", seed, e.body)
			}
		case splitToolEnd:
			if e.complete && e.body != body {
				t.Fatalf("seed %d: final body %q != deltas %q", seed, e.body, body)
			}
			r.calls = append(r.calls, splitCall{e.name, e.body, e.complete})
			r.order = append(r.order, "tool")
		case splitHalt:
			r.halted = true
		}
	}
	return r
}

func forSeeds(t *testing.T, text string, check func(t *testing.T, r splitResult)) {
	t.Helper()
	for seed := 1; seed <= 200; seed++ {
		check(t, runSplit(t, text, seed))
	}
}

func TestSplitterPlainText(t *testing.T) {
	in := "Hello <b>world</b>, a < b and tool_call is a word. <tool_calls> too."
	forSeeds(t, in, func(t *testing.T, r splitResult) {
		if r.text != in || len(r.calls) != 0 {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestSplitterTextCallText(t *testing.T) {
	in := "Let me look.\n<tool_call name=\"bash\">\n<parameter name=\"command\">ls -la</parameter>\n</tool_call>\nDone."
	forSeeds(t, in, func(t *testing.T, r splitResult) {
		want := splitCall{"bash", "\n<parameter name=\"command\">ls -la</parameter>\n", true}
		if r.text != "Let me look.\n\nDone." || len(r.calls) != 1 || r.calls[0] != want || strings.Join(r.order, ",") != "text,tool,text" {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestSplitterMultipleCalls(t *testing.T) {
	in := "<tool_call name='read' id=\"1\"><parameter name=\"path\">a.go</parameter></tool_call>\n<tool_call name=\"read\"><parameter name=\"path\">b.go</parameter></tool_call>"
	forSeeds(t, in, func(t *testing.T, r splitResult) {
		if len(r.calls) != 2 || r.calls[0].name != "read" || r.calls[1].name != "read" || strings.TrimSpace(r.text) != "" {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestSplitterHaltsOnInventedResult(t *testing.T) {
	in := "<tool_call name=\"bash\"><parameter name=\"command\">ls</parameter></tool_call>\n<tool_result name=\"bash\">fake</tool_result> more"
	forSeeds(t, in, func(t *testing.T, r splitResult) {
		if len(r.calls) != 1 || !r.halted || strings.Contains(r.text, "fake") {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestSplitterUnterminatedCall(t *testing.T) {
	in := "ok <tool_call name=\"write\"><parameter name=\"path\">x</parameter><parameter name=\"content\">abc"
	forSeeds(t, in, func(t *testing.T, r splitResult) {
		if len(r.calls) != 1 || r.calls[0].complete || !strings.HasSuffix(r.calls[0].body, "abc") {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestSplitterTrailingPrefixBecomesText(t *testing.T) {
	if r := runSplit(t, "see <tool_ca", 3); r.text != "see <tool_ca" {
		t.Fatalf("got %+v", r)
	}
}

// The fenced forms are verbatim Gemma 3 output seen with llama.cpp.
func TestSplitterFencedForms(t *testing.T) {
	tests := []struct {
		name, in, text string
		call           splitCall
		bodyHas        string
	}{
		{"attributes on the fence line", "```tool_call name=\"edit\">\n<parameter name=\"path\">app.go</parameter>\n</tool_call>\n```", "", splitCall{name: "edit", complete: true}, "\n<parameter name=\"path\">"},
		{"inner tool wrapper", "```tool_call\n<tool name=\"edit\">\n<parameter name=\"path\">app.go</parameter>\n</tool>\n```", "", splitCall{"", "<tool name=\"edit\">\n<parameter name=\"path\">app.go</parameter>\n", true}, ""},
		{"inner tool_call tag", "```tool_call\n<tool_call name=\"read\">\n<parameter name=\"path\">a.go</parameter>\n</tool_call>\n```", "", splitCall{complete: true}, "<tool_call name=\"read\">"},
		{"JSON ends at the fence", "```tool_call\n{\"name\": \"bash\", \"arguments\": {\"command\": \"ls\"}}\n```\nafter", "\nafter", splitCall{"", "{\"name\": \"bash\", \"arguments\": {\"command\": \"ls\"}}", true}, ""},
		{"text around", "Sure.\n```tool_call name=\"read\">\n<parameter name=\"path\">a</parameter>\n</tool_call>\n```\nDone.", "Sure.\n\nDone.", splitCall{name: "read", complete: true}, ""},
		{"backticks inside a value", "```tool_call name=\"write\">\n<parameter name=\"content\">\n```js\nx()\n```\n</parameter>\n</tool_call>\n```", "", splitCall{name: "write", complete: true}, "```js\nx()\n```"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			forSeeds(t, tc.in, func(t *testing.T, r splitResult) {
				if r.text != tc.text || len(r.calls) != 1 {
					t.Fatalf("got %+v", r)
				}
				c := r.calls[0]
				if c.name != tc.call.name || c.complete != tc.call.complete || (tc.call.body != "" && c.body != tc.call.body) || !strings.Contains(c.body, tc.bodyHas) {
					t.Fatalf("call = %+v, want %+v", c, tc.call)
				}
			})
		})
	}
}

func TestSplitterOrdinaryFencesPassThrough(t *testing.T) {
	in := "Example:\n```go\nx := 1\n```\nand ```tool_calls``` too."
	forSeeds(t, in, func(t *testing.T, r splitResult) {
		if r.text != in || len(r.calls) != 0 {
			t.Fatalf("got %+v", r)
		}
	})
}
