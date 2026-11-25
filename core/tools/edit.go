package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
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
		if count == 1 {
			text = strings.Replace(text, oldText, newText, 1)
			continue
		}
		if count > 1 {
			return errorResult("edit %d: old_text matches %d times in %s (at lines %s). Include more surrounding context to make it unique.",
				i+1, count, shown, matchLines(text, oldText))
		}
		// Spaces and tabs at the ends of lines never change what the text
		// means, and models often get them wrong, so match without them.
		loose, offsets := trimLineEnds(text)
		looseOld, _ := trimLineEnds(oldText)
		switch n := strings.Count(loose, looseOld); {
		case looseOld == "" || n == 0:
			return errorResult("edit %d: old_text not found in %s. %s", i+1, shown, notFoundHint(text, oldText))
		case n > 1:
			return errorResult("edit %d: old_text matches %d times in %s (at lines %s) when trailing whitespace is ignored. Include more surrounding context to make it unique.",
				i+1, n, shown, matchLines(loose, looseOld))
		}
		at := strings.Index(loose, looseOld)
		start, end := offsets[at], offsets[at+len(looseOld)-1]+1
		text = text[:start] + newText + text[end:]
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

// trimLineEnds removes spaces and tabs at the end of each line of s. It also
// returns, for each byte of the result, that byte's offset in s.
func trimLineEnds(s string) (string, []int) {
	var b strings.Builder
	offsets := make([]int, 0, len(s))
	for start := 0; ; {
		end := strings.IndexByte(s[start:], '\n')
		line := s[start:]
		if end >= 0 {
			line = s[start : start+end]
		}
		kept := strings.TrimRight(line, " \t")
		b.WriteString(kept)
		for i := range len(kept) {
			offsets = append(offsets, start+i)
		}
		if end < 0 {
			return b.String(), offsets
		}
		b.WriteByte('\n')
		offsets = append(offsets, start+end)
		start += end + 1
	}
}

// notFoundHint explains a failed match. It finds where old_text was meant
// to match (a match that differs only in whitespace, or else the region most
// like it), names the first line that differs, and shows the file's text
// there as it is, without line numbers, so it can be copied.
func notFoundHint(text, oldText string) string {
	fileLines := strings.Split(text, "\n")
	oldLines := strings.Split(strings.Trim(oldText, "\n"), "\n")

	start, kind := -1, ""
	for i := 0; i+len(oldLines) <= len(fileLines); i++ {
		ok := true
		for j, ol := range oldLines {
			if strings.TrimSpace(fileLines[i+j]) != strings.TrimSpace(ol) {
				ok = false
				break
			}
		}
		if ok {
			start, kind = i, "A match that differs only in whitespace or indentation"
			break
		}
	}
	if start < 0 {
		// Align the first non-empty line of old_text with the file line
		// most like it.
		first, firstAt := "", 0
		for j, l := range oldLines {
			if strings.TrimSpace(l) != "" {
				first, firstAt = strings.TrimSpace(l), j
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
		start, kind = max(0, best-firstAt), "The most similar text"
	}
	end := min(len(fileLines), start+len(oldLines))

	var b strings.Builder
	fmt.Fprintf(&b, "%s is at line %d.", kind, start+1)
	for j, ol := range oldLines {
		fileLine := "(end of file)"
		if start+j < len(fileLines) {
			fileLine = strconv.Quote(fileLines[start+j])
		}
		if start+j >= len(fileLines) || strings.TrimRight(fileLines[start+j], " \t") != strings.TrimRight(ol, " \t") {
			fmt.Fprintf(&b, " Line %d of old_text differs from line %d of the file:\n  old_text: %s\n  file:     %s\n",
				j+1, start+j+1, strconv.Quote(ol), fileLine)
			break
		}
	}
	from, to := max(0, start-2), min(len(fileLines), end+2)
	fmt.Fprintf(&b, "\nLines %d-%d of the file, as they are:\n%s\nCopy old_text from this text exactly.",
		from+1, to, strings.Join(fileLines[from:to], "\n"))
	return b.String()
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
