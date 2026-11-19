// Package compact implements context compaction: choosing a cut point and
// summarizing everything before it.
package compact

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/provider"
)

//go:embed prompt.md
var defaultInstructions string

// Instructions returns the summarization instructions, honoring
// .rho/COMPACT.md and ~/.rho/COMPACT.md overrides.
func Instructions(p config.Paths) string {
	for _, path := range []string{filepath.Join(p.ProjectDir, "COMPACT.md"), filepath.Join(p.Home, "COMPACT.md")} {
		if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
			return string(data)
		}
	}
	return defaultInstructions
}

// ShouldCompact reports whether tokens exceed the usable window.
func ShouldCompact(tokens, window, reserve int) bool {
	if window <= 0 {
		return false
	}
	limit := window - reserve
	if limit < window/2 {
		limit = window / 2
	}
	return tokens > limit
}

// FindCut chooses the index of the first message to keep verbatim, keeping
// roughly keepTokens of recent messages. Cuts never fall on a tool result
// (which would separate it from its call) or a synthetic message without
// an entry id. It returns false if nothing can be compacted.
func FindCut(msgs []provider.Message, ids []string, keepTokens int) (int, bool) {
	valid := func(i int) bool {
		return msgs[i].Role != provider.RoleToolResult && i < len(ids) && ids[i] != ""
	}
	acc := 0
	cut := -1
	for i := len(msgs) - 1; i >= 1; i-- {
		acc += msgs[i].EstimateTokens()
		if valid(i) {
			cut = i
			if acc >= keepTokens {
				break
			}
		}
	}
	if cut < 1 {
		return 0, false
	}
	return cut, true
}

// Summarize asks the model to summarize msgs.
func Summarize(ctx context.Context, prov provider.Provider, model config.Model, msgs []provider.Message, instructions string) (string, provider.Usage, error) {
	if len(msgs) == 0 {
		return "", provider.Usage{}, errors.New("nothing to summarize")
	}
	transcript := Transcript(msgs, 4000)
	req := provider.Request{
		Model:  model.APIName(),
		System: "You summarize coding-agent conversations so the work can continue in a fresh context. Output only the summary.",
		Messages: []provider.Message{provider.UserText(
			"Here is the conversation to summarize:\n\n<conversation>\n" + transcript + "\n</conversation>\n\n" + instructions)},
		MaxTokens:       min(max(model.MaxOutput, 1024), 8192),
		ReasoningEffort: model.ReasoningEffort,
	}
	resp, err := prov.Stream(ctx, req, func(provider.StreamEvent) {})
	if err != nil {
		return "", resp.Usage, err
	}
	summary := strings.TrimSpace(resp.Message.TextContent())
	if summary == "" {
		return "", resp.Usage, errors.New("the model returned an empty summary")
	}
	return summary, resp.Usage, nil
}

// Transcript renders messages as plain text. Tool results longer than
// maxResult characters are shortened.
func Transcript(msgs []provider.Message, maxResult int) string {
	var b strings.Builder
	for _, m := range msgs {
		for _, c := range m.Content {
			switch c.Type {
			case provider.BlockText:
				fmt.Fprintf(&b, "[%s]\n%s\n\n", m.Role, c.Text)
			case provider.BlockToolCall:
				args := string(c.Args)
				var compactArgs any
				if json.Unmarshal(c.Args, &compactArgs) == nil {
					if out, err := json.Marshal(compactArgs); err == nil {
						args = string(out)
					}
				}
				fmt.Fprintf(&b, "[tool call: %s]\n%s\n\n", c.Name, shorten(args, maxResult))
			case provider.BlockToolResult:
				var rb strings.Builder
				for _, rc := range c.Content {
					switch rc.Type {
					case provider.BlockText:
						rb.WriteString(rc.Text)
					case provider.BlockImage:
						rb.WriteString("[image]")
					}
				}
				label := "tool result"
				if c.IsError {
					label = "tool error"
				}
				fmt.Fprintf(&b, "[%s]\n%s\n\n", label, shorten(rb.String(), maxResult))
			case provider.BlockImage:
				fmt.Fprintf(&b, "[%s]\n[image]\n\n", m.Role)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func shorten(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	half := n / 2
	return s[:half] + fmt.Sprintf("\n[... %d characters omitted ...]\n", len(s)-n) + s[len(s)-half:]
}
