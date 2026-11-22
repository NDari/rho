package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
)

// Read limits.
const (
	ReadDefaultLimit = 2000
	ReadMaxBytes     = 50 * 1024
	ReadMaxLineLen   = 2000
	ReadMaxImage     = 10 * 1024 * 1024
)

var imageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// Read reads files.
type Read struct{}

// Spec implements Tool.
func (Read) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "read",
		Description: "Read a file. Text files are returned with line numbers. " +
			fmt.Sprintf("By default up to %d lines are returned; use offset and limit to page through large files. ", ReadDefaultLimit) +
			"Images (png, jpg, gif, webp) are returned as images when the model supports them.",
		Parameters: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path, absolute or relative to the working directory"},
    "offset": {"type": "integer", "description": "1-based line number to start reading from"},
    "limit": {"type": "integer", "description": "Maximum number of lines to read"}
  },
  "required": ["path"]
}`),
	}
}

// ReadOnly implements Tool.
func (Read) ReadOnly() bool { return true }

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// Execute implements Tool.
func (Read) Execute(_ context.Context, env *Env, raw json.RawMessage, _ func(string)) Result {
	var a readArgs
	if err := decodeArgs(raw, &a); err != nil {
		return errorResult("%v", err)
	}
	if a.Path == "" {
		return errorResult("path is required")
	}
	path := ResolvePath(env.Cwd, a.Path)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return errorResult("file not found: %s", a.Path)
	}
	if err != nil {
		return errorResult("cannot read %s: %v", a.Path, err)
	}
	if info.IsDir() {
		return errorResult("%s is a directory; use ls to list it", a.Path)
	}
	shown := DisplayPath(env.Cwd, path)

	if mime, ok := imageTypes[strings.ToLower(filepath.Ext(path))]; ok {
		return readImage(env, path, shown, mime, info.Size())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return errorResult("cannot read %s: %v", a.Path, err)
	}
	if isBinary(data) {
		return errorResult("%s is a binary file (%s, %s); not shown", a.Path, http.DetectContentType(data), humanSize(info.Size()))
	}
	return readText(string(data), a, shown)
}

func readImage(env *Env, path, shown, mime string, size int64) Result {
	if !env.SupportsImages {
		return errorResult("%s is an image (%s, %s), but the current model does not support images", shown, mime, humanSize(size))
	}
	if size > ReadMaxImage {
		return errorResult("%s is too large to send (%s; limit %s)", shown, humanSize(size), humanSize(ReadMaxImage))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errorResult("cannot read %s: %v", shown, err)
	}
	return Result{
		Content: []provider.Block{
			provider.Text(fmt.Sprintf("Image %s (%s, %s):", shown, mime, humanSize(size))),
			{Type: provider.BlockImage, MIME: mime, Data: data},
		},
		Display: &events.Display{Kind: "image", Path: shown, Summary: humanSize(size)},
	}
}

func readText(content string, a readArgs, shown string) Result {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	start := 1
	if a.Offset > 0 {
		start = a.Offset
	}
	if total == 0 {
		return textResult("(empty file)", &events.Display{Kind: "text", Path: shown, Summary: "empty"})
	}
	if start > total {
		return errorResult("offset %d is past the end of %s (%d lines)", start, shown, total)
	}
	limit := ReadDefaultLimit
	if a.Limit > 0 {
		limit = a.Limit
	}
	end := min(start-1+limit, total)

	var b strings.Builder
	var plain strings.Builder
	truncatedBytes := false
	longLines := false
	for i := start - 1; i < end; i++ {
		line := lines[i]
		if len(line) > ReadMaxLineLen {
			line = line[:ReadMaxLineLen] + " [line truncated]"
			longLines = true
		}
		entry := fmt.Sprintf("%6d\t%s\n", i+1, line)
		if b.Len()+len(entry) > ReadMaxBytes {
			end = i
			truncatedBytes = true
			break
		}
		b.WriteString(entry)
		plain.WriteString(line)
		plain.WriteByte('\n')
	}
	if end < total {
		fmt.Fprintf(&b, "\n[Showing lines %d-%d of %d", start, end, total)
		if truncatedBytes {
			fmt.Fprintf(&b, " (output limit %s reached)", humanSize(ReadMaxBytes))
		}
		fmt.Fprintf(&b, ". Use offset=%d to continue.]\n", end+1)
	}
	if longLines {
		fmt.Fprintf(&b, "[Some lines were longer than %d characters and were truncated.]\n", ReadMaxLineLen)
	}
	summary := fmt.Sprintf("%d lines", end-start+1)
	if start > 1 || end < total {
		summary = fmt.Sprintf("lines %d-%d of %d", start, end, total)
	}
	return textResult(b.String(), &events.Display{
		Kind: "text", Path: shown, Text: plain.String(), Summary: summary, Truncated: end < total,
	})
}

// isBinary reports whether data looks like a binary file.
func isBinary(data []byte) bool {
	sample := data
	if len(sample) > 8000 {
		sample = sample[:8000]
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	// Allow a truncated multi-byte rune at the end of the sample.
	for i := 0; i < 4 && len(sample) > 0 && !utf8.Valid(sample); i++ {
		sample = sample[:len(sample)-1]
	}
	return !utf8.Valid(sample)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
