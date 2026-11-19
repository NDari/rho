package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
)

// Edit replaces exact text in a file.
type Edit struct{}

// Spec implements Tool.
func (Edit) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "edit",
		Description: "Edit a file by replacing exact text. Each old_text must match the file exactly once " +
			"(including whitespace and indentation); include enough surrounding lines to make it unique. " +
			"Edits are applied in order, and either all succeed or the file is left unchanged.",
		Parameters: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path, absolute or relative to the working directory"},
    "edits": {
      "type": "array",
      "description": "Replacements to apply in order",
      "items": {
        "type": "object",
        "properties": {
          "old_text": {"type": "string", "description": "Exact text to find; must occur exactly once"},
          "new_text": {"type": "string", "description": "Replacement text"}
        },
        "required": ["old_text", "new_text"]
      }
    }
  },
  "required": ["path", "edits"]
}`),
	}
}

// ReadOnly implements Tool.
func (Edit) ReadOnly() bool { return false }

type editArgs struct {
	Path  string `json:"path"`
	Edits []struct {
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	} `json:"edits"`
	// Some models send a single replacement at the top level.
	OldText *string `json:"old_text"`
	NewText *string `json:"new_text"`
}

// Execute implements Tool.
func (Edit) Execute(_ context.Context, env *Env, raw json.RawMessage, _ func(string)) Result {
	var a editArgs
	if err := decodeArgs(raw, &a); err != nil {
		return errorResult("%v", err)
	}
	if a.Path == "" {
		return errorResult("path is required")
	}
	if a.OldText != nil && a.NewText != nil {
		a.Edits = append(a.Edits, struct {
			OldText string `json:"old_text"`
			NewText string `json:"new_text"`
		}{*a.OldText, *a.NewText})
	}
	if len(a.Edits) == 0 {
		return errorResult("edits must contain at least one {old_text, new_text} pair")
	}
	path := ResolvePath(env.Cwd, a.Path)
	shown := DisplayPath(env.Cwd, path)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return errorResult("file not found: %s (use write to create new files)", shown)
	}
	if err != nil {
		return errorResult("cannot read %s: %v", shown, err)
	}
	if isBinary(data) {
		return errorResult("%s is a binary file and cannot be edited", shown)
	}

	original := string(data)
	bom := ""
	if strings.HasPrefix(original, "\uFEFF") {
		bom = "\uFEFF"
		original = original[len(bom):]
	}
	crlf := strings.Contains(original, "\r\n")
	before := normalizeNewlines(original)
	text := before

	for i, e := range a.Edits {
		oldText := normalizeNewlines(e.OldText)
		newText := normalizeNewlines(e.NewText)
		if oldText == "" {
			return errorResult("edit %d: old_text is empty", i+1)
		}
		if oldText == newText {
			return errorResult("edit %d: old_text and new_text are identical", i+1)
		}
		count := strings.Count(text, oldText)
		switch {
		case count == 0:
			return errorResult("edit %d: old_text not found in %s. %s", i+1, shown, notFoundHint(text, oldText))
		case count > 1:
			return errorResult("edit %d: old_text matches %d times in %s (at lines %s). Include more surrounding context to make it unique.",
				i+1, count, shown, matchLines(text, oldText))
		}
		text = strings.Replace(text, oldText, newText, 1)
	}

	out := text
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	if err := atomicWrite(path, []byte(bom+out)); err != nil {
		return errorResult("cannot write %s: %v", shown, err)
	}
	diff := MakeDiff(before, text, 3)
	msg := fmt.Sprintf("Edited %s: %d edit(s) applied (+%d -%d lines)", shown, len(a.Edits), diff.Added, diff.Removed)
	return textResult(msg, &events.Display{Kind: "diff", Path: shown, Diff: diff})
}

func lineOf(text string, idx int) int { return strings.Count(text[:idx], "\n") + 1 }

func matchLines(text, sub string) string {
	var nums []string
	off := 0
	for len(nums) < 10 {
		i := strings.Index(text[off:], sub)
		if i < 0 {
			break
		}
		nums = append(nums, fmt.Sprint(lineOf(text, off+i)))
		off += i + len(sub)
	}
	return strings.Join(nums, ", ")
}

// notFoundHint explains a failed match: either a whitespace-only difference
// or the most similar region of the file.
func notFoundHint(text, oldText string) string {
	fileLines := strings.Split(text, "\n")
	oldLines := strings.Split(strings.Trim(oldText, "\n"), "\n")

	// Whitespace-insensitive line match.
	for i := 0; i+len(oldLines) <= len(fileLines); i++ {
		ok := true
		for j, ol := range oldLines {
			if strings.TrimSpace(fileLines[i+j]) != strings.TrimSpace(ol) {
				ok = false
				break
			}
		}
		if ok {
			return fmt.Sprintf("A match that differs only in whitespace/indentation exists at line %d:\n%s\nCopy the text exactly.",
				i+1, numbered(fileLines, i, i+len(oldLines)))
		}
	}

	// Closest line to the first non-empty line of old_text.
	var first string
	for _, l := range oldLines {
		if strings.TrimSpace(l) != "" {
			first = strings.TrimSpace(l)
			break
		}
	}
	best, bestScore := -1, 0.0
	for i, l := range fileLines {
		if s := similarity(strings.TrimSpace(l), first); s > bestScore {
			best, bestScore = i, s
		}
	}
	if best < 0 || bestScore < 0.5 {
		return "Read the file again to see its current content."
	}
	from := max(0, best-3)
	to := min(len(fileLines), best+len(oldLines)+3)
	return fmt.Sprintf("The most similar text is near line %d:\n%s", best+1, numbered(fileLines, from, to))
}

func numbered(lines []string, from, to int) string {
	var b strings.Builder
	for i := from; i < to && i < len(lines); i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, lines[i])
	}
	return strings.TrimRight(b.String(), "\n")
}

// similarity is a cheap token-overlap score in [0, 1].
func similarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	ta, tb := strings.Fields(a), strings.Fields(b)
	set := map[string]int{}
	for _, t := range ta {
		set[t]++
	}
	common := 0
	for _, t := range tb {
		if set[t] > 0 {
			set[t]--
			common++
		}
	}
	return 2 * float64(common) / float64(len(ta)+len(tb))
}
