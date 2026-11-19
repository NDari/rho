package xmltools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
)

var testTools = []provider.ToolSpec{
	{Name: "read", Description: "Read a file.", Parameters: json.RawMessage(readSchema)},
	{Name: "edit", Description: "Edit a file.", Parameters: json.RawMessage(editSchema)},
}

func history() []provider.Message {
	return []provider.Message{
		provider.UserText("fix it"),
		{Role: provider.RoleAssistant, Content: []provider.Block{
			{Type: provider.BlockThinking, Text: "hmm", Provider: "fake"},
			provider.Text("Looking."),
			{Type: provider.BlockToolCall, ID: "c1", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)},
		}},
		{Role: provider.RoleToolResult, Content: []provider.Block{provider.ToolResult("c1", "package a", false)}},
		provider.UserText("and hurry"),
	}
}

func TestRequestRewrite(t *testing.T) {
	for _, inUser := range []bool{false, true} {
		var got provider.Request
		fp := fake.New("fake", fake.Step{Text: "ok", Check: func(r provider.Request) { got = r }})
		p := Wrap(fp, Options{SystemInUser: inUser, StopSequences: true})
		if _, err := p.Stream(context.Background(), provider.Request{System: "BASE", Tools: testTools, Messages: history()}, func(provider.StreamEvent) {}); err != nil {
			t.Fatal(err)
		}
		if len(got.Tools) != 0 {
			t.Error("native tools were sent")
		}
		if len(got.Stop) != 1 || got.Stop[0] != "<tool_result" {
			t.Errorf("stop = %v", got.Stop)
		}
		if len(got.Messages) != 3 {
			t.Fatalf("messages = %d, want 3 (user, assistant, merged user): %+v", len(got.Messages), got.Messages)
		}
		first := got.Messages[0].TextContent()
		if inUser {
			if got.System != "" || !strings.HasPrefix(first, "<instructions>\nBASE\n\n# Tool use") || !strings.HasSuffix(first, "</instructions>\n\nfix it") {
				t.Errorf("system in user: system=%q first=%q", got.System, first)
			}
		} else if !strings.HasPrefix(got.System, "BASE\n\n# Tool use") || first != "fix it" {
			t.Errorf("system=%q first=%q", got.System, first)
		}
		asst := got.Messages[1]
		if asst.Content[0].Type != provider.BlockThinking {
			t.Error("thinking was not kept for the adapter")
		}
		if want := "Looking.\n\n<tool_call name=\"read\">\n<parameter name=\"path\">a.go</parameter>\n</tool_call>"; asst.TextContent() != want {
			t.Errorf("assistant = %q", asst.TextContent())
		}
		if want := "<tool_result name=\"read\" status=\"ok\">\npackage a\n</tool_result>\n\nand hurry"; got.Messages[2].TextContent() != want {
			t.Errorf("merged user = %q", got.Messages[2].TextContent())
		}
	}
}

func TestResponseParsing(t *testing.T) {
	text := "Reading it.\n<tool_call name=\"read\">\n<parameter name=\"path\">a.go</parameter>\n<parameter name=\"limit\">5</parameter>\n</tool_call>\n<tool_call name=\"edit\">\n<parameter name=\"path\">a.go</parameter>\n</tool_call>\n<tool_result name=\"read\">invented</tool_result>"
	fp := fake.New("fake", fake.Step{Thinking: "plan", Text: text})
	var streamed strings.Builder
	var starts []string
	resp, err := Wrap(fp, Options{}).Stream(context.Background(), provider.Request{Tools: testTools, Messages: []provider.Message{provider.UserText("hi")}}, func(e provider.StreamEvent) {
		switch e.Kind {
		case provider.EventTextDelta:
			streamed.WriteString(e.Text)
		case provider.EventToolCallStart:
			starts = append(starts, e.Name)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(streamed.String(), "tool_call") || strings.Contains(streamed.String(), "invented") {
		t.Errorf("streamed text = %q", streamed.String())
	}
	if strings.Join(starts, ",") != "read,edit" {
		t.Errorf("tool starts = %v", starts)
	}
	c := resp.Message.Content
	if len(c) != 4 || c[0].Type != provider.BlockThinking || c[1].Text != "Reading it." {
		t.Fatalf("content = %+v", c)
	}
	if c[2].Name != "read" || string(c[2].Args) != `{"path":"a.go","limit":5}` || c[2].ArgsError != "" {
		t.Errorf("read call = %+v", c[2])
	}
	if c[3].Name != "edit" || c[3].ArgsError != "" {
		// Missing required arguments are left to the tool's own validation.
		t.Errorf("edit call = %+v", c[3])
	}
	if resp.StopReason != provider.StopToolUse || resp.Message.Meta.StopReason != provider.StopToolUse {
		t.Errorf("stop = %s", resp.StopReason)
	}
}

func TestMalformedCallGetsAnError(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "<tool_call>please read a.go</tool_call>"})
	resp, err := Wrap(fp, Options{}).Stream(context.Background(), provider.Request{Tools: testTools, Messages: []provider.Message{provider.UserText("hi")}}, func(provider.StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || !strings.Contains(calls[0].ArgsError, "no name") || !strings.Contains(calls[0].ArgsError, "please read a.go") {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestPlainAnswer(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "All done."})
	resp, err := Wrap(fp, Options{}).Stream(context.Background(), provider.Request{Tools: testTools, Messages: []provider.Message{provider.UserText("hi")}}, func(provider.StreamEvent) {})
	if err != nil || resp.Message.TextContent() != "All done." || resp.StopReason != provider.StopEnd {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
}
