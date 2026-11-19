package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
)

// highlighter colors source code with chroma.
type highlighter struct {
	style *chroma.Style
	cache map[chroma.TokenType]lipgloss.Style
}

func newHighlighter(styleName string) *highlighter {
	st := chromastyles.Get(styleName)
	if st == nil {
		st = chromastyles.Fallback
	}
	return &highlighter{style: st, cache: map[chroma.TokenType]lipgloss.Style{}}
}

func lexerFor(path string) chroma.Lexer {
	if path == "" {
		return nil
	}
	l := lexers.Match(path)
	if l == nil {
		return nil
	}
	return chroma.Coalesce(l)
}

func (h *highlighter) tokenStyle(t chroma.TokenType) lipgloss.Style {
	if s, ok := h.cache[t]; ok {
		return s
	}
	e := h.style.Get(t)
	s := lipgloss.NewStyle()
	if e.Colour.IsSet() {
		s = s.Foreground(lipgloss.Color(e.Colour.String()))
	}
	if e.Bold == chroma.Yes {
		s = s.Bold(true)
	}
	if e.Italic == chroma.Yes {
		s = s.Italic(true)
	}
	h.cache[t] = s
	return s
}

// span is a piece of a line with a style.
type span struct {
	text  string
	style lipgloss.Style
}

// tokens splits one line into styled spans. Without a lexer the whole line
// is a single plain span.
func (h *highlighter) tokens(lexer chroma.Lexer, line string) []span {
	if lexer == nil || line == "" {
		return []span{{text: line, style: lipgloss.NewStyle()}}
	}
	it, err := lexer.Tokenise(nil, line)
	if err != nil {
		return []span{{text: line, style: lipgloss.NewStyle()}}
	}
	var out []span
	for tok := it(); tok != chroma.EOF; tok = it() {
		v := strings.TrimSuffix(tok.Value, "\n")
		if v == "" {
			continue
		}
		out = append(out, span{text: v, style: h.tokenStyle(tok.Type)})
	}
	return out
}

// Highlight returns a highlighted version of each line of code.
func (h *highlighter) Highlight(path string, lines []string) []string {
	lexer := lexerFor(path)
	out := make([]string, len(lines))
	for i, l := range lines {
		l = expandTabs(l)
		if lexer == nil {
			out[i] = l
			continue
		}
		var b strings.Builder
		for _, sp := range h.tokens(lexer, l) {
			b.WriteString(sp.style.Render(sp.text))
		}
		out[i] = b.String()
	}
	return out
}

func expandTabs(s string) string { return strings.ReplaceAll(s, "\t", "    ") }
