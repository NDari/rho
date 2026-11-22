package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
)

// LsDefaultLimit is the default number of entries ls returns.
const LsDefaultLimit = 500

// Ls lists a directory.
type Ls struct{}

// Spec implements Tool.
func (Ls) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "ls",
		Description: "List a directory: one entry per line, sorted by name, with directories ending in / and symbolic links in @. " +
			"Hidden and ignored entries are included. " +
			fmt.Sprintf("Stops after %d entries unless limit says otherwise.", LsDefaultLimit),
		Parameters: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Directory to list, absolute or relative to the working directory (default: the working directory)"},
    "limit": {"type": "integer", "description": "Maximum number of entries to return"}
  }
}`),
	}
}

// ReadOnly implements Tool.
func (Ls) ReadOnly() bool { return true }

type lsArgs struct {
	Path  string `json:"path"`
	Limit int    `json:"limit"`
}

// Execute implements Tool.
func (Ls) Execute(_ context.Context, env *Env, raw json.RawMessage, _ func(string)) Result {
	var a lsArgs
	if err := decodeArgs(raw, &a); err != nil {
		return errorResult("%v", err)
	}
	dir, info, err := searchDir(env, a.Path)
	if err != nil {
		return errorResult("%v", err)
	}
	if !info.IsDir() {
		return errorResult("%s is a file; use read to see it", a.Path)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errorResult("cannot list %s: %v", a.Path, err)
	}
	limit := LsDefaultLimit
	if a.Limit > 0 {
		limit = a.Limit
	}
	if len(entries) == 0 {
		return textResult("(empty directory)", &events.Display{Kind: "text", Summary: "empty"})
	}
	var b strings.Builder
	for _, e := range entries[:min(len(entries), limit)] {
		b.WriteString(e.Name())
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			b.WriteByte('@')
		case e.IsDir():
			b.WriteByte('/')
		}
		b.WriteByte('\n')
	}
	text := strings.TrimSuffix(b.String(), "\n")
	summary := plural(len(entries), "entry", "entries")
	if len(entries) > limit {
		text += fmt.Sprintf("\n\n[Showing %d of %d entries. Raise limit to see more.]", limit, len(entries))
		summary = fmt.Sprintf("%d of %d entries", limit, len(entries))
	}
	return textResult(text, &events.Display{Kind: "text", Text: text, Summary: summary, Truncated: len(entries) > limit})
}
