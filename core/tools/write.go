package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
)

// Write creates or overwrites files.
type Write struct{}

// Spec implements Tool.
func (Write) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "write",
		Description: "Write content to a file, creating it (and parent directories) or overwriting it. " +
			"Prefer edit for changes to existing files.",
		Parameters: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path, absolute or relative to the working directory"},
    "content": {"type": "string", "description": "The complete new file content"}
  },
  "required": ["path", "content"]
}`),
	}
}

// ReadOnly implements Tool.
func (Write) ReadOnly() bool { return false }

type writeArgs struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

// Execute implements Tool.
func (Write) Execute(_ context.Context, env *Env, raw json.RawMessage, _ func(string)) Result {
	var a writeArgs
	if err := decodeArgs(raw, &a); err != nil {
		return errorResult("%v", err)
	}
	if a.Path == "" {
		return errorResult("path is required")
	}
	if a.Content == nil {
		return errorResult("content is required")
	}
	path := ResolvePath(env.Cwd, a.Path)
	shown := DisplayPath(env.Cwd, path)

	old, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errorResult("cannot read existing %s: %v", shown, err)
	}
	if existed && isBinary(old) {
		old = nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errorResult("cannot create directory for %s: %v", shown, err)
	}
	if err := atomicWrite(path, []byte(*a.Content)); err != nil {
		return errorResult("cannot write %s: %v", shown, err)
	}
	lines := strings.Count(*a.Content, "\n")
	if *a.Content != "" && !strings.HasSuffix(*a.Content, "\n") {
		lines++
	}
	msg := fmt.Sprintf("Created %s (%d lines)", shown, lines)
	if existed {
		msg = fmt.Sprintf("Wrote %s (%d lines)", shown, lines)
	}
	return textResult(msg, &events.Display{
		Kind: "diff",
		Path: shown,
		Diff: MakeDiff(normalizeNewlines(string(old)), normalizeNewlines(*a.Content), 3),
	})
}

// atomicWrite writes data to a temporary file next to path and renames it
// into place, preserving the existing file mode.
func atomicWrite(path string, data []byte) error {
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".rho-*")
	if err != nil {
		// Fall back to a direct write (e.g. unwritable directory but writable file).
		return os.WriteFile(path, data, mode)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return lockedFileHint(err)
	}
	return nil
}

func normalizeNewlines(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
