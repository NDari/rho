package compact

import (
	"strings"
	"testing"

	"github.com/NDari/rho/core/provider"
)

func msg(role provider.Role, n int) provider.Message {
	return provider.Message{Role: role, Content: []provider.Block{provider.Text(strings.Repeat("x", n*4))}}
}

func TestFindCutKeepsRecentAndAvoidsToolResults(t *testing.T) {
	msgs := []provider.Message{
		msg(provider.RoleUser, 1000),
		msg(provider.RoleAssistant, 1000),
		msg(provider.RoleUser, 1000),
		msg(provider.RoleAssistant, 1000),
		msg(provider.RoleToolResult, 1000),
		msg(provider.RoleAssistant, 1000),
	}
	ids := []string{"a", "b", "c", "d", "e", "f"}
	cut, ok := FindCut(msgs, ids, 2500)
	if !ok {
		t.Fatal("expected a cut")
	}
	if msgs[cut].Role == provider.RoleToolResult {
		t.Errorf("cut at a tool result (%d)", cut)
	}
	if cut != 3 {
		t.Errorf("cut = %d, want 3", cut)
	}
}

func TestFindCutSkipsSyntheticSummary(t *testing.T) {
	msgs := []provider.Message{msg(provider.RoleUser, 10), msg(provider.RoleAssistant, 10)}
	if _, ok := FindCut(msgs, []string{"", "b"}, 1); !ok {
		t.Error("expected cut at index 1")
	}
	if _, ok := FindCut(msgs[:1], []string{""}, 1); ok {
		t.Error("nothing to compact with a single message")
	}
}

func TestShouldCompact(t *testing.T) {
	if !ShouldCompact(50000, 64000, 16000) || ShouldCompact(40000, 64000, 16000) {
		t.Error("threshold wrong")
	}
	// Small windows never drop below half the window.
	if ShouldCompact(7000, 16000, 16000) || !ShouldCompact(9000, 16000, 16000) {
		t.Error("small window handling wrong")
	}
}

func TestOutputLimit(t *testing.T) {
	tests := []struct {
		name                                 string
		maxOutput, window, tokens, estimated int
		want                                 int
	}{
		{"far from the window", 16384, 128000, 50000, 2000, 16384},
		{"64k just under the trigger", 16384, 64000, 47000, 2000, 12904},
		{"large estimated tail widens the margin", 16384, 64000, 40000, 20000, 14000},
		{"1M window near the trigger", 16384, 1000000, 983000, 2000, 12904},
		{"never below the floor", 16384, 64000, 62000, 0, MinOutput},
		{"model limit below the floor", 2048, 64000, 63000, 0, 2048},
		{"unknown window", 16384, 0, 50000, 0, 16384},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OutputLimit(tt.maxOutput, tt.window, tt.tokens, tt.estimated); got != tt.want {
				t.Errorf("OutputLimit = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTranscriptShortensResults(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleToolResult, Content: []provider.Block{provider.ToolResult("a", strings.Repeat("y", 10000), false)}},
	}
	out := Transcript(msgs, 100)
	if len(out) > 300 || !strings.Contains(out, "omitted") {
		t.Errorf("transcript not shortened: %d chars", len(out))
	}
}

func TestKeepTokens(t *testing.T) {
	if got := KeepTokens(20000, 200000, 16000); got != 20000 {
		t.Errorf("large window keep = %d", got)
	}
	if got := KeepTokens(20000, 6000, 16000); got != 1500 {
		t.Errorf("tiny window keep = %d", got)
	}
}
