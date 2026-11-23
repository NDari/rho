package compact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
)

func TestInstructions(t *testing.T) {
	tests := []struct {
		name      string
		project   string // contents of .rho/COMPACT.md ("" = absent)
		home      string // contents of ~/.rho/COMPACT.md ("" = absent)
		noProject bool
		want      string // "" = the built-in instructions
	}{
		{name: "built-in"},
		{name: "home override", home: "home rules", want: "home rules"},
		{name: "project wins over home", project: "project rules", home: "home rules", want: "project rules"},
		{name: "blank project file falls back to home", project: " \n\t\n", home: "home rules", want: "home rules"},
		{name: "blank home file falls back to built-in", home: "\n\n"},
		{name: "project ignored", project: "project rules", noProject: true},
		{name: "project ignored falls back to home", project: "project rules", home: "home rules", noProject: true, want: "home rules"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			p := config.Paths{Home: filepath.Join(dir, "home"), Cwd: dir, ProjectRoot: dir, ProjectDir: filepath.Join(dir, ".rho"), NoProject: tt.noProject}
			for path, content := range map[string]string{
				filepath.Join(p.ProjectDir, "COMPACT.md"): tt.project,
				filepath.Join(p.Home, "COMPACT.md"):       tt.home,
			} {
				if content == "" {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := Instructions(p)
			want := tt.want
			if want == "" {
				want = defaultInstructions
			}
			if got != want {
				t.Errorf("Instructions = %q, want %q", got, want)
			}
		})
	}
	if !strings.Contains(defaultInstructions, "summary") {
		t.Error("built-in instructions do not ask for a summary")
	}
}

func conversation() []provider.Message {
	call := provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{
		provider.Text("let me look"),
		{Type: provider.BlockToolCall, ID: "c1", Name: "read", Args: []byte(`{ "path" : "main.go" }`)},
	}}
	result := provider.Message{Role: provider.RoleToolResult, Content: []provider.Block{
		provider.ToolResult("c1", "package main", false),
	}}
	return []provider.Message{provider.UserText("fix the build"), call, result, {Role: provider.RoleAssistant, Content: []provider.Block{provider.Text("fixed")}}}
}

func TestSummarize(t *testing.T) {
	usage := provider.Usage{InputTokens: 500, OutputTokens: 40}
	overflow := &provider.Error{Kind: provider.ErrContextOverflow, StatusCode: 400, Message: "context_length_exceeded"}
	tests := []struct {
		name      string
		maxOutput int
		step      fake.Step
		want      string
		wantErr   string
		wantIs    error
		wantKind  provider.ErrorKind
		wantUsage provider.Usage
		wantMax   int
	}{
		{name: "summary", maxOutput: 4096, step: fake.Step{Text: "\n  goal: fix the build  \n", Usage: usage}, want: "goal: fix the build", wantUsage: usage, wantMax: 4096},
		{name: "small output limit raised", maxOutput: 100, step: fake.Step{Text: "s"}, want: "s", wantMax: 1024},
		{name: "large output limit capped", maxOutput: 100000, step: fake.Step{Text: "s"}, want: "s", wantMax: 8192},
		{name: "empty summary", maxOutput: 4096, step: fake.Step{Text: "   ", Usage: usage}, wantErr: "empty summary", wantUsage: usage, wantMax: 4096},
		{name: "only tool calls", maxOutput: 4096, step: fake.Step{Calls: []fake.Call{{Name: "bash", Args: map[string]string{"command": "ls"}}}}, wantErr: "empty summary", wantMax: 4096},
		{name: "context overflow", maxOutput: 4096, step: fake.Step{Err: overflow}, wantIs: overflow, wantKind: provider.ErrContextOverflow, wantMax: 4096},
		{name: "transient error", maxOutput: 4096, step: fake.Step{Err: &provider.Error{Kind: provider.ErrTransient, StatusCode: 503}}, wantKind: provider.ErrTransient, wantErr: "HTTP 503", wantMax: 4096},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fp := fake.New("fake", tt.step)
			model := config.Model{ID: "local", Name: "served-name", Provider: "fake", MaxOutput: tt.maxOutput, ReasoningEffort: "low"}
			got, u, err := Summarize(context.Background(), fp, model, conversation(), "KEEP FILE NAMES")
			if tt.wantErr == "" && tt.wantIs == nil {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatalf("Summarize = %q, want an error", got)
				}
				if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want %q", err, tt.wantErr)
				}
				if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
					t.Errorf("err = %v, want %v", err, tt.wantIs)
				}
				if provider.KindOf(err) != tt.wantKind {
					t.Errorf("error kind = %v, want %v (callers decide on retries and fallbacks by kind)", provider.KindOf(err), tt.wantKind)
				}
				if errors.Is(err, ErrNotASummary) {
					t.Errorf("Summarize returned ErrNotASummary, which callers treat as a cue to fall back to Summarize")
				}
			}
			if got != tt.want {
				t.Errorf("summary = %q, want %q", got, tt.want)
			}
			if u != tt.wantUsage {
				t.Errorf("usage = %+v, want %+v", u, tt.wantUsage)
			}

			if len(fp.Requests) != 1 {
				t.Fatalf("%d requests, want 1", len(fp.Requests))
			}
			req := fp.Requests[0]
			if req.Model != "served-name" || req.MaxTokens != tt.wantMax || req.ReasoningEffort != "low" {
				t.Errorf("request model=%q max=%d effort=%q, want served-name %d low", req.Model, req.MaxTokens, req.ReasoningEffort, tt.wantMax)
			}
			if len(req.Tools) != 0 {
				t.Errorf("summary request offers %d tools, want none", len(req.Tools))
			}
			if len(req.Messages) != 1 || req.Messages[0].Role != provider.RoleUser {
				t.Fatalf("summary request should be one user message, got %d", len(req.Messages))
			}
			body := req.Messages[0].TextContent()
			for _, s := range []string{"<conversation>", "fix the build", `[tool call: read]` + "\n" + `{"path":"main.go"}`, "[tool result]\npackage main", "fixed", "</conversation>"} {
				if !strings.Contains(body, s) {
					t.Errorf("transcript lacks %q:\n%s", s, body)
				}
			}
			if !strings.HasSuffix(body, "KEEP FILE NAMES") {
				t.Errorf("instructions should end the request:\n%s", body)
			}
		})
	}
}

func TestSummarizeNothing(t *testing.T) {
	fp := fake.New("fake")
	for name, f := range map[string]func() error{
		"Summarize": func() error {
			_, _, err := Summarize(context.Background(), fp, config.Model{ID: "m"}, nil, "x")
			return err
		},
		"SummarizeInContext": func() error {
			_, _, err := SummarizeInContext(context.Background(), fp, config.Model{ID: "m"}, provider.Request{System: "sys"}, "x")
			return err
		},
	} {
		if err := f(); err == nil || !strings.Contains(err.Error(), "nothing to summarize") {
			t.Errorf("%s with no messages: err = %v, want nothing to summarize", name, err)
		}
	}
	if len(fp.Requests) != 0 {
		t.Errorf("%d requests sent for an empty conversation", len(fp.Requests))
	}
}

func TestSummarizeInContext(t *testing.T) {
	usage := provider.Usage{InputTokens: 9000, CacheReadTokens: 8000, OutputTokens: 50}
	overflow := &provider.Error{Kind: provider.ErrContextOverflow, StatusCode: 400, Message: "exceeds the available context"}
	tests := []struct {
		name      string
		step      fake.Step
		want      string
		wantIs    error
		wantUsage provider.Usage
	}{
		{name: "summary", step: fake.Step{Text: " the summary \n", Usage: usage}, want: "the summary", wantUsage: usage},
		{name: "tool call instead", step: fake.Step{Text: "I will check first", Calls: []fake.Call{{Name: "read", Args: map[string]string{"path": "a"}}}, Usage: usage}, wantIs: ErrNotASummary, wantUsage: usage},
		{name: "blank answer", step: fake.Step{Text: "\n\n", Usage: usage}, wantIs: ErrNotASummary, wantUsage: usage},
		{name: "thinking only", step: fake.Step{Thinking: "hmm"}, wantIs: ErrNotASummary},
		{name: "context overflow", step: fake.Step{Err: overflow}, wantIs: overflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fp := fake.New("fake", tt.step)
			msgs := conversation()
			tools := []provider.ToolSpec{{Name: "read", Description: "read a file", Parameters: []byte(`{}`)}}
			req := provider.Request{Model: "served", System: "agent system prompt", Messages: msgs, Tools: tools, MaxTokens: 123, ReasoningEffort: "high"}
			got, u, err := SummarizeInContext(context.Background(), fp, config.Model{ID: "m", MaxOutput: 16384}, req, "KEEP FILE NAMES")
			if tt.wantIs == nil {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, tt.wantIs) {
				t.Fatalf("err = %v, want %v", err, tt.wantIs)
			}
			if got != tt.want {
				t.Errorf("summary = %q, want %q", got, tt.want)
			}
			if u != tt.wantUsage {
				t.Errorf("usage = %+v, want %+v", u, tt.wantUsage)
			}
			if errors.Is(tt.wantIs, overflow) && provider.KindOf(err) != provider.ErrContextOverflow {
				t.Errorf("overflow lost its kind: %v", provider.KindOf(err))
			}

			sent := fp.Requests[0]
			// The cached prefix is untouched: same system prompt, tools
			// and messages, followed by one instruction.
			if sent.System != req.System || sent.Model != "served" || sent.ReasoningEffort != "high" || len(sent.Tools) != 1 {
				t.Errorf("request prefix changed: system=%q model=%q effort=%q tools=%d", sent.System, sent.Model, sent.ReasoningEffort, len(sent.Tools))
			}
			if sent.MaxTokens != 8192 {
				t.Errorf("MaxTokens = %d, want 8192 (capped)", sent.MaxTokens)
			}
			if len(sent.Messages) != len(msgs)+1 {
				t.Fatalf("%d messages sent, want %d", len(sent.Messages), len(msgs)+1)
			}
			for i := range msgs {
				if sent.Messages[i].TextContent() != msgs[i].TextContent() || sent.Messages[i].Role != msgs[i].Role {
					t.Errorf("message %d changed", i)
				}
			}
			last := sent.Messages[len(msgs)]
			if last.Role != provider.RoleUser || !strings.Contains(last.TextContent(), "Do not call any tools") || !strings.Contains(last.TextContent(), "KEEP FILE NAMES") {
				t.Errorf("final instruction = %+v", last)
			}
		})
	}
}

func TestSummarizeInContextDoesNotShareBacking(t *testing.T) {
	// Spare capacity in the caller's slice must not be written to.
	msgs := make([]provider.Message, 1, 4)
	msgs[0] = provider.UserText("hello")
	fp := fake.New("fake", fake.Step{Text: "s"})
	if _, _, err := SummarizeInContext(context.Background(), fp, config.Model{ID: "m"}, provider.Request{Messages: msgs}, "x"); err != nil {
		t.Fatal(err)
	}
	if spare := msgs[:2][1]; spare.Role != "" || len(spare.Content) != 0 {
		t.Errorf("the instruction was written into the caller's backing array: %+v", spare)
	}
}
