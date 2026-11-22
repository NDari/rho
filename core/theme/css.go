package theme

import (
	"fmt"
	"sort"
	"strings"

	"github.com/alecthomas/chroma/v2"
)

// CSSVars returns the CSS custom properties (without "--") the GUI and the
// HTML export style themselves with. The hl-* properties are token colors
// taken from the theme's Chroma style, for highlighters other than Chroma.
func (t Theme) CSSVars() map[string]string {
	v := map[string]string{
		"bg":        t.Background,
		"bg-soft":   t.Surface,
		"bg-raised": t.UserBg,
		"border":    t.Border,
		"fg":        t.Text,
		"fg-muted":  t.Muted,
		"fg-faint":  t.LineNumber,
		"accent":    t.Accent,
		"accent-fg": Contrast(t.Accent),
		"user-bg":   t.UserBg,
		"link":      t.Accent,
		"ok":        t.Success,
		"warn":      t.Warning,
		"err":       t.Error,
		"info":      t.Accent,
		"tool":      t.ToolName,
		"thinking":  t.Thinking,
		"add-bg":    t.DiffAddBg,
		"add-emph":  t.DiffAddEmph,
		"add-fg":    t.DiffAddSign,
		"del-bg":    t.DiffDelBg,
		"del-emph":  t.DiffDelEmph,
		"del-fg":    t.DiffDelSign,
		"code-bg":   t.Surface,
		"search-bg": t.SearchBg,
		"search-fg": t.SearchFg,
	}
	st := t.ChromaStyle()
	color := func(types ...chroma.TokenType) string {
		for _, tt := range types {
			if e := st.Get(tt); e.Colour.IsSet() {
				return e.Colour.String()
			}
		}
		return t.Text
	}
	v["hl-keyword"] = color(chroma.Keyword)
	v["hl-string"] = color(chroma.LiteralString)
	v["hl-number"] = color(chroma.LiteralNumber)
	v["hl-comment"] = color(chroma.Comment)
	v["hl-title"] = color(chroma.NameFunction)
	v["hl-type"] = color(chroma.KeywordType, chroma.NameClass)
	v["hl-attr"] = color(chroma.NameAttribute, chroma.NameVariable, chroma.NameTag)
	v["hl-meta"] = color(chroma.CommentPreproc, chroma.NameDecorator)
	return v
}

// CSSRule returns the theme's CSS properties as one rule for selector,
// with color-scheme set to match the background.
func (t Theme) CSSRule(selector string) string {
	vars := t.CSSVars()
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	scheme := "light"
	if t.IsDark() {
		scheme = "dark"
	}
	fmt.Fprintf(&b, "%s { color-scheme: %s;", selector, scheme)
	for _, k := range keys {
		fmt.Fprintf(&b, " --%s: %s;", k, vars[k])
	}
	b.WriteString(" }")
	return b.String()
}
