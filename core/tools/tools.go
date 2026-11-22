// Package tools implements Rho's built-in tools: read, write, edit, bash,
// grep, find and ls.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// Concurrent is implemented by tools whose calls may run at the same time
// as other such calls from the same response: read-only tools that keep no
// state. Calls to other tools run one at a time, in order.
type Concurrent interface {
	Concurrent() bool
}

// IsConcurrent reports whether calls to t may run in parallel.
func IsConcurrent(t Tool) bool {
	c, ok := t.(Concurrent)
	return ok && c.Concurrent() && t.ReadOnly()
}

// Builtins returns every built-in tool.
func Builtins() []Tool {
	return []Tool{Read{}, Write{}, Edit{}, Bash{}, Grep{}, Find{}, Ls{}}
}

// DefaultTools are the built-in tools offered when tools.enabled is not
// set.
var DefaultTools = []string{"read", "write", "edit", "bash"}

// searchTools are offered whenever bash is not.
var searchTools = []string{"grep", "find", "ls"}

// Select returns the built-in tools named in enabled (DefaultTools when
// enabled is nil), less those named in disabled, in Builtins order. When
// bash is not offered, grep, find and ls are added unless disabled; auto
// names the tools added that way.
func Select(enabled, disabled []string) (ts []Tool, auto []string, err error) {
	if enabled == nil {
		enabled = DefaultTools
	}
	all := Builtins()
	var names []string
	for _, t := range all {
		names = append(names, t.Spec().Name)
	}
	for _, n := range enabled {
		if !slices.Contains(names, n) {
			return nil, nil, fmt.Errorf("unknown built-in tool %q (built-in tools: %s)", n, strings.Join(names, ", "))
		}
	}
	addSearch := !slices.Contains(enabled, "bash") || slices.Contains(disabled, "bash")
	for _, t := range all {
		n := t.Spec().Name
		if slices.Contains(disabled, n) {
			continue
		}
		switch {
		case slices.Contains(enabled, n):
			ts = append(ts, t)
		case addSearch && slices.Contains(searchTools, n):
			ts = append(ts, t)
			auto = append(auto, n)
		}
	}
	return ts, auto, nil
}

// Names returns the names of ts.
func Names(ts []Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Spec().Name
	}
	return out
}

// Without returns ts without the tools named in names.
func Without(ts []Tool, names []string) []Tool {
	var out []Tool
	for _, t := range ts {
		if !slices.Contains(names, t.Spec().Name) {
			out = append(out, t)
		}
	}
	return out
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
