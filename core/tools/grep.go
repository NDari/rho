package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
)

// Grep limits.
const (
	GrepDefaultLimit = 100
	GrepMaxContext   = 10
	GrepMaxLineLen   = 500
	// grepMaxScanLine is the longest line searched; the rest of a file
	// with a longer line is skipped.
	grepMaxScanLine = 1 << 20
)

// Grep searches file contents.
type Grep struct{}

// Spec implements Tool.
func (Grep) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "grep",
		Description: "Search file contents with a regular expression. Prints path:line:text for each matching line, in path order. " +
			"Skips binary files, .git, and files ignored by .gitignore or .ignore; hidden files are searched. " +
			fmt.Sprintf("Stops after %d matching lines unless limit says otherwise; lines are cut at %d characters.", GrepDefaultLimit, GrepMaxLineLen),
		Parameters: schema(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Regular expression (RE2 syntax, as in ripgrep: no lookaround or backreferences)"},
    "path": {"type": "string", "description": "File or directory to search, absolute or relative to the working directory (default: the working directory)"},
    "glob": {"type": "string", "description": "Only search files matching this glob, such as *.go or src/**/*.ts; a glob without a slash matches file names at any depth"},
    "ignore_case": {"type": "boolean", "description": "Match case-insensitively"},
    "literal": {"type": "boolean", "description": "Treat pattern as plain text, not a regular expression"},
    "context": {"type": "integer", "description": "Lines of context to show before and after each match (at most 10)"},
    "limit": {"type": "integer", "description": "Maximum number of matching lines to return"}
  },
  "required": ["pattern"]
}`),
	}
}

// ReadOnly implements Tool.
func (Grep) ReadOnly() bool { return true }

// Concurrent implements Concurrent.
func (Grep) Concurrent() bool { return true }

type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignore_case"`
	Literal    bool   `json:"literal"`
	Context    int    `json:"context"`
	Limit      int    `json:"limit"`
}

// Execute implements Tool.
func (Grep) Execute(ctx context.Context, env *Env, raw json.RawMessage, _ func(string)) Result {
	var a grepArgs
	if err := decodeArgs(raw, &a); err != nil {
		return errorResult("%v", err)
	}
	if a.Pattern == "" {
		return errorResult("pattern is required")
	}
	expr := a.Pattern
	if a.Literal {
		expr = regexp.QuoteMeta(expr)
	}
	if a.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return errorResult("invalid regular expression: %v (set literal to search for plain text)", err)
	}
	var g *glob
	if a.Glob != "" {
		gl, err := newGlob(a.Glob)
		if err != nil {
			return errorResult("%v", err)
		}
		g = &gl
	}
	root, info, err := searchDir(env, a.Path)
	if err != nil {
		return errorResult("%v", err)
	}
	s := &grepSearch{re: re, cwd: env.Cwd, context: min(max(a.Context, 0), GrepMaxContext), limit: GrepDefaultLimit}
	if a.Limit > 0 {
		s.limit = a.Limit
	}
	if !info.IsDir() {
		s.file(root)
	} else {
		err = newWalker(root).walk(ctx, root, func(p string, d fs.DirEntry) error {
			if !d.Type().IsRegular() || g != nil && !g.match(relSlash(root, p)) {
				return nil
			}
			s.file(p)
			if s.full {
				return errStop
			}
			return nil
		})
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return errorResult("search cancelled")
		}
		return errorResult("cannot search %s: %v", a.Path, err)
	}
	return s.result()
}

type grepLine struct {
	n    int
	text string
}

type grepSearch struct {
	re      *regexp.Regexp
	cwd     string
	context int
	limit   int

	out      strings.Builder
	matches  int
	files    int
	full     bool // the match limit was reached
	fullSize bool // the output size limit was reached
	partial  int  // files with a line too long to search
	lastFile string
	lastLine int
}

// file searches one file, unless it looks binary.
func (s *grepSearch) file(p string) {
	f, err := os.Open(p)
	if err != nil {
		return
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64*1024)
	if head, _ := br.Peek(8000); isBinary(head) {
		return
	}
	shown := DisplayPath(s.cwd, p)
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 64*1024), grepMaxScanLine)
	var before []grepLine
	after, n, matched := 0, 0, false
	for sc.Scan() {
		n++
		line := strings.TrimSuffix(sc.Text(), "\r")
		loc := s.re.FindStringIndex(line)
		switch {
		case loc != nil:
			if s.matches == s.limit {
				s.full = true
				return
			}
			for _, b := range before {
				s.emit(shown, b.n, '-', b.text, nil)
			}
			before = before[:0]
			s.emit(shown, n, ':', line, loc)
			if s.full {
				return
			}
			s.matches++
			if !matched {
				matched = true
				s.files++
			}
			after = s.context
		case after > 0:
			s.emit(shown, n, '-', line, nil)
			after--
		case s.context > 0:
			before = append(before, grepLine{n, line})
			if len(before) > s.context {
				before = before[1:]
			}
		}
		if s.full {
			return
		}
	}
	if errors.Is(sc.Err(), bufio.ErrTooLong) {
		s.partial++
	}
}

// emit writes one output line: path:n:text for a match (loc is where it
// matched), path-n-text for context. Groups that do not follow each other
// are separated by "--" when context is shown.
func (s *grepSearch) emit(file string, n int, sep byte, text string, loc []int) {
	var b strings.Builder
	if s.context > 0 && s.out.Len() > 0 && (file != s.lastFile || n != s.lastLine+1) {
		b.WriteString("--\n")
	}
	fmt.Fprintf(&b, "%s%c%d%c%s\n", file, sep, n, sep, cutLine(text, loc))
	if s.out.Len()+b.Len() > ReadMaxBytes {
		s.full, s.fullSize = true, true
		return
	}
	s.out.WriteString(b.String())
	s.lastFile, s.lastLine = file, n
}

// cutLine shortens a long line to GrepMaxLineLen bytes around the match
// at loc (or from the start), on rune boundaries.
func cutLine(line string, loc []int) string {
	if len(line) <= GrepMaxLineLen {
		return line
	}
	start := 0
	if loc != nil {
		start = max(0, min(loc[0]-GrepMaxLineLen/4, len(line)-GrepMaxLineLen))
	}
	end := start + GrepMaxLineLen
	for start > 0 && !utf8.RuneStart(line[start]) {
		start--
	}
	for end < len(line) && !utf8.RuneStart(line[end]) {
		end--
	}
	out := line[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(line) {
		out += "…"
	}
	return out
}

func (s *grepSearch) result() Result {
	if s.matches == 0 {
		return textResult("no matches", &events.Display{Kind: "text", Summary: "no matches"})
	}
	text := strings.TrimSuffix(s.out.String(), "\n")
	summary := plural(s.matches, "match", "matches") + " in " + plural(s.files, "file", "files")
	var notes []string
	switch {
	case s.fullSize:
		notes = append(notes, fmt.Sprintf("[Output limit %s reached after %s. Narrow the search with path or glob.]", humanSize(ReadMaxBytes), plural(s.matches, "match", "matches")))
	case s.full:
		notes = append(notes, fmt.Sprintf("[Stopped at %s. Narrow the search with path or glob, or raise limit.]", plural(s.limit, "match", "matches")))
	}
	if s.partial > 0 {
		notes = append(notes, fmt.Sprintf("[%s had a line over %s; the rest of each was not searched.]", plural(s.partial, "file", "files"), humanSize(grepMaxScanLine)))
	}
	if len(notes) > 0 {
		text += "\n\n" + strings.Join(notes, "\n")
	}
	if s.full {
		summary += ", stopped"
	}
	return textResult(text, &events.Display{Kind: "text", Text: text, Summary: summary, Truncated: s.full})
}
