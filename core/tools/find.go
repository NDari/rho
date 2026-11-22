package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
)

// FindDefaultLimit is the default number of paths find returns.
const FindDefaultLimit = 1000

// Find lists files whose paths match a glob.
type Find struct{}

// Spec implements Tool.
func (Find) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "find",
		Description: "Find files and directories whose paths match a glob, such as *.go or src/**/*_test.go. " +
			"Prints one path per line in path order; directories end in /. " +
			"Skips .git and paths ignored by .gitignore or .ignore; hidden files are included. " +
			fmt.Sprintf("Stops after %d paths unless limit says otherwise.", FindDefaultLimit),
		Parameters: schema(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Glob matched against paths relative to path; a glob without a slash matches names at any depth. Supports *, ?, [abc], {a,b} and **"},
    "path": {"type": "string", "description": "Directory to search, absolute or relative to the working directory (default: the working directory)"},
    "limit": {"type": "integer", "description": "Maximum number of paths to return"}
  },
  "required": ["pattern"]
}`),
	}
}

// ReadOnly implements Tool.
func (Find) ReadOnly() bool { return true }

// Concurrent implements Concurrent.
func (Find) Concurrent() bool { return true }

type findArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Limit   int    `json:"limit"`
}

// Execute implements Tool.
func (Find) Execute(ctx context.Context, env *Env, raw json.RawMessage, _ func(string)) Result {
	var a findArgs
	if err := decodeArgs(raw, &a); err != nil {
		return errorResult("%v", err)
	}
	if a.Pattern == "" {
		return errorResult("pattern is required")
	}
	g, err := newGlob(a.Pattern)
	if err != nil {
		return errorResult("%v", err)
	}
	root, info, err := searchDir(env, a.Path)
	if err != nil {
		return errorResult("%v", err)
	}
	if !info.IsDir() {
		return errorResult("%s is a file, not a directory", a.Path)
	}
	limit := FindDefaultLimit
	if a.Limit > 0 {
		limit = a.Limit
	}
	var b strings.Builder
	n, full := 0, false
	err = newWalker(root).walk(ctx, root, func(p string, d fs.DirEntry) error {
		if !g.match(relSlash(root, p)) {
			return nil
		}
		line := DisplayPath(env.Cwd, p)
		if d.IsDir() {
			line += "/"
		}
		if n == limit || b.Len()+len(line)+1 > ReadMaxBytes {
			full = true
			return errStop
		}
		b.WriteString(line)
		b.WriteByte('\n')
		n++
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return errorResult("search cancelled")
		}
		return errorResult("cannot search %s: %v", a.Path, err)
	}
	if n == 0 {
		return textResult("no matching paths", &events.Display{Kind: "text", Summary: "no matches"})
	}
	text := strings.TrimSuffix(b.String(), "\n")
	summary := plural(n, "path", "paths")
	if full {
		text += fmt.Sprintf("\n\n[Stopped at %s. Narrow the pattern or path, or raise limit.]", plural(n, "path", "paths"))
		summary += ", stopped"
	}
	return textResult(text, &events.Display{Kind: "text", Text: text, Summary: summary, Truncated: full})
}
