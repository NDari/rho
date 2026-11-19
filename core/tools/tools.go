// Package tools implements Rho's built-in tools: read, write, edit and bash.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
)

// Env is the execution environment shared by tools.
type Env struct {
	// Cwd is the session working directory.
	Cwd string
	// SupportsImages reports whether the current model accepts images.
	SupportsImages bool
	// BashTimeout and BashMaxTimeout are in seconds.
	BashTimeout    int
	BashMaxTimeout int
	// Shell overrides the shell used by bash.
	Shell string
}

// Result is what a tool returns.
type Result struct {
	// Content is sent to the model.
	Content []provider.Block
	IsError bool
	// Display is presentation data for frontends only.
	Display *events.Display
}

// Tool is a capability offered to the model.
type Tool interface {
	Spec() provider.ToolSpec
	// ReadOnly reports whether the tool never modifies anything.
	ReadOnly() bool
	// Execute runs the tool. progress may be called with live output.
	Execute(ctx context.Context, env *Env, args json.RawMessage, progress func(string)) Result
}

// Builtins returns the four built-in tools.
func Builtins() []Tool {
	return []Tool{Read{}, Write{}, Edit{}, Bash{}}
}

// ReadOnlyTools filters ts to read-only tools.
func ReadOnlyTools(ts []Tool) []Tool {
	var out []Tool
	for _, t := range ts {
		if t.ReadOnly() {
			out = append(out, t)
		}
	}
	return out
}

func errorResult(format string, a ...any) Result {
	return Result{Content: []provider.Block{provider.Text(fmt.Sprintf(format, a...))}, IsError: true}
}

func textResult(s string, d *events.Display) Result {
	return Result{Content: []provider.Block{provider.Text(s)}, Display: d}
}

// decodeArgs unmarshals tool arguments with a model-friendly error.
func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return fmt.Errorf("arguments are not a valid JSON object: %s", truncateString(s, 200))
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// ResolvePath turns a model-supplied path into an absolute path.
func ResolvePath(cwd, p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	p = platformPath(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// DisplayPath shortens p relative to cwd for presentation.
func DisplayPath(cwd, p string) string {
	if rel, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return p
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func schema(s string) json.RawMessage { return json.RawMessage(s) }
