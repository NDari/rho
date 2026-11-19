package tui

import (
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	glamourstyles "charm.land/glamour/v2/styles"
)

// markdown renders markdown with glamour, caching renderers by width.
type markdown struct {
	theme     Theme
	renderers map[int]*glamour.TermRenderer
}

func newMarkdown(t Theme) *markdown {
	return &markdown{theme: t, renderers: map[int]*glamour.TermRenderer{}}
}

func uptr(u uint) *uint { return &u }

func (m *markdown) renderer(width int) *glamour.TermRenderer {
	if r, ok := m.renderers[width]; ok {
		return r
	}
	var cfg ansi.StyleConfig
	if m.theme.IsDark() {
		cfg = glamourstyles.DarkStyleConfig
	} else {
		cfg = glamourstyles.LightStyleConfig
	}
	cfg.Document.Margin = uptr(0)
	cfg.Document.BlockPrefix = ""
	cfg.Document.BlockSuffix = ""
	cfg.CodeBlock.Theme = m.theme.Chroma
	cfg.CodeBlock.Chroma = nil
	// Take headings, links and inline code from the theme, so markdown
	// matches the rest of the interface.
	t := m.theme
	cfg.Document.Color = &t.Text
	cfg.Heading.Color = &t.Accent
	cfg.H1.Color = &t.StatusBg
	cfg.H1.BackgroundColor = &t.Accent
	cfg.Link.Color = &t.Accent
	cfg.LinkText.Color = &t.Accent
	cfg.Code.Color = &t.Warning
	cfg.Code.BackgroundColor = &t.UserBg
	cfg.HorizontalRule.Color = &t.Subtle
	r, err := glamour.NewTermRenderer(glamour.WithStyles(cfg), glamour.WithWordWrap(width))
	if err != nil {
		return nil
	}
	m.renderers[width] = r
	return r
}

// Render renders markdown to lines.
func (m *markdown) Render(src string, width int) []string {
	if width < 10 {
		width = 10
	}
	r := m.renderer(width)
	if r == nil {
		return strings.Split(src, "\n")
	}
	out, err := r.Render(src)
	if err != nil {
		return strings.Split(src, "\n")
	}
	return trimBlankLines(strings.Split(strings.TrimRight(out, "\n"), "\n"))
}

func trimBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(stripANSI(lines[0])) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(stripANSI(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// stableSplit returns the length of the prefix of src that ends at a block
// boundary (a blank line outside a code fence). The prefix can be rendered
// once and cached while the rest is still streaming.
func stableSplit(src string) int {
	inFence := false
	last := 0
	pos := 0
	for pos < len(src) {
		nl := strings.IndexByte(src[pos:], '\n')
		if nl < 0 {
			break
		}
		line := src[pos : pos+nl]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		end := pos + nl + 1
		if !inFence && trimmed == "" && end < len(src) {
			last = end
		}
		pos = end
	}
	return last
}

// streamingMarkdown caches the rendering of the stable prefix of a growing
// markdown document.
type streamingMarkdown struct {
	prefixLen   int
	prefixWidth int
	prefixLines []string
}

func (s *streamingMarkdown) render(md *markdown, src string, width int, final bool) []string {
	if final {
		s.prefixLen = 0
		return md.Render(src, width)
	}
	split := stableSplit(src)
	if split != s.prefixLen || width != s.prefixWidth {
		s.prefixLen = split
		s.prefixWidth = width
		s.prefixLines = nil
		if split > 0 {
			s.prefixLines = md.Render(src[:split], width)
		}
	}
	tail := src[split:]
	lines := append([]string(nil), s.prefixLines...)
	if strings.TrimSpace(tail) != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, md.Render(tail, width)...)
	}
	return lines
}
