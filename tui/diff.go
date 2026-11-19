package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sergi/go-diff/diffmatchpatch"

	"github.com/NDari/rho/core/events"
)

// diffRenderer draws unified or side-by-side diffs with syntax
// highlighting and word-level emphasis.
type diffRenderer struct {
	theme Theme
	hl    *highlighter
	st    styles
}

// rng is a byte range [start, end) within a line.
type rng struct{ start, end int }

// row is one rendered diff row in unified form.
type diffRow struct {
	kind     rune
	oldNo    int
	newNo    int
	text     string
	emphasis []rng
}

func (d *diffRenderer) rows(diff *events.Diff) [][]diffRow {
	var hunks [][]diffRow
	for _, h := range diff.Hunks {
		var rows []diffRow
		oldNo, newNo := h.OldStart, h.NewStart
		for _, l := range h.Lines {
			r := diffRow{kind: l.Kind, text: expandTabs(l.Text)}
			switch l.Kind {
			case events.LineRemoved:
				r.oldNo = oldNo
				oldNo++
			case events.LineAdded:
				r.newNo = newNo
				newNo++
			default:
				r.oldNo, r.newNo = oldNo, newNo
				oldNo++
				newNo++
			}
			rows = append(rows, r)
		}
		addEmphasis(rows)
		hunks = append(hunks, rows)
	}
	return hunks
}

// addEmphasis pairs removed and added lines within each change block and
// marks the changed words.
func addEmphasis(rows []diffRow) {
	for i := 0; i < len(rows); {
		if rows[i].kind != events.LineRemoved {
			i++
			continue
		}
		start := i
		for i < len(rows) && rows[i].kind == events.LineRemoved {
			i++
		}
		mid := i
		for i < len(rows) && rows[i].kind == events.LineAdded {
			i++
		}
		removed, added := rows[start:mid], rows[mid:i]
		for k := 0; k < len(removed) && k < len(added); k++ {
			removed[k].emphasis, added[k].emphasis = wordDiff(removed[k].text, added[k].text)
		}
	}
}

// wordDiff returns the changed ranges in a and b, or nothing when the lines
// are too different for emphasis to help.
func wordDiff(a, b string) ([]rng, []rng) {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(a, b, false)
	diffs = dmp.DiffCleanupSemantic(diffs)
	var ra, rb []rng
	pa, pb, common := 0, 0, 0
	for _, df := range diffs {
		n := len(df.Text)
		switch df.Type {
		case diffmatchpatch.DiffEqual:
			pa += n
			pb += n
			common += n
		case diffmatchpatch.DiffDelete:
			ra = append(ra, rng{pa, pa + n})
			pa += n
		case diffmatchpatch.DiffInsert:
			rb = append(rb, rng{pb, pb + n})
			pb += n
		}
	}
	longest := max(len(a), len(b))
	if longest == 0 || float64(common)/float64(longest) < 0.4 {
		return nil, nil
	}
	return ra, rb
}

// renderLine draws text with syntax colors, a background, and stronger
// background on emphasized ranges. The result is padded to width.
func (d *diffRenderer) renderLine(lexer chroma.Lexer, text string, emph []rng, bg, emphBg string, width int) string {
	spans := d.hl.tokens(lexer, text)
	var b strings.Builder
	pos := 0
	for _, sp := range spans {
		t := sp.text
		for len(t) > 0 {
			inEmph, until := emphAt(emph, pos, pos+len(t))
			piece := t[:until-pos]
			st := sp.style
			if inEmph && emphBg != "" {
				st = st.Background(c(emphBg))
			} else if bg != "" {
				st = st.Background(c(bg))
			}
			b.WriteString(st.Render(piece))
			t = t[len(piece):]
			pos += len(piece)
		}
	}
	out := b.String()
	w := ansi.StringWidth(out)
	if w > width {
		return ansi.Truncate(out, width, "…")
	}
	pad := strings.Repeat(" ", width-w)
	if bg != "" {
		pad = lipgloss.NewStyle().Background(c(bg)).Render(pad)
	}
	return out + pad
}

// emphAt reports whether pos is inside an emphasized range and where the
// current run (emphasized or not) ends, capped at limit.
func emphAt(emph []rng, pos, limit int) (bool, int) {
	end := limit
	for _, r := range emph {
		if pos >= r.start && pos < r.end {
			return true, min(r.end, limit)
		}
		if r.start > pos && r.start < end {
			end = r.start
		}
	}
	return false, end
}

// Render draws the diff at the given width.
func (d *diffRenderer) Render(diff *events.Diff, path string, width int, sideBySide bool, maxRows int) []string {
	if diff == nil || len(diff.Hunks) == 0 {
		return []string{d.st.muted.Render("(no changes)")}
	}
	hunks := d.rows(diff)
	lexer := lexerFor(path)
	maxNo := 0
	for _, h := range hunks {
		for _, r := range h {
			maxNo = max(maxNo, r.oldNo, r.newNo)
		}
	}
	numW := len(fmt.Sprint(maxNo))
	var out []string
	for hi, rows := range hunks {
		if hi > 0 {
			out = append(out, d.st.subtle.Render(strings.Repeat("┄", min(width, 40))))
		}
		if sideBySide {
			out = append(out, d.sideBySide(rows, lexer, numW, width)...)
		} else {
			out = append(out, d.unified(rows, lexer, numW, width)...)
		}
	}
	if maxRows > 0 && len(out) > maxRows {
		hidden := len(out) - maxRows
		out = append(out[:maxRows], d.st.muted.Render(fmt.Sprintf("… %d more lines (ctrl+o to expand)", hidden)))
	}
	return out
}

func (d *diffRenderer) num(n, w int) string {
	if n == 0 {
		return strings.Repeat(" ", w)
	}
	return fmt.Sprintf("%*d", w, n)
}

func (d *diffRenderer) unified(rows []diffRow, lexer chroma.Lexer, numW, width int) []string {
	var out []string
	gutter := numW*2 + 4
	textW := max(10, width-gutter)
	for _, r := range rows {
		var sign, bg, emphBg string
		signStyle := d.st.lineNum
		switch r.kind {
		case events.LineAdded:
			sign, bg, emphBg = "+", d.theme.DiffAddBg, d.theme.DiffAddEmph
			signStyle = lipgloss.NewStyle().Foreground(c(d.theme.DiffAddSign)).Background(c(bg))
		case events.LineRemoved:
			sign, bg, emphBg = "-", d.theme.DiffDelBg, d.theme.DiffDelEmph
			signStyle = lipgloss.NewStyle().Foreground(c(d.theme.DiffDelSign)).Background(c(bg))
		default:
			sign = " "
		}
		nums := d.st.lineNum.Render(d.num(r.oldNo, numW) + " " + d.num(r.newNo, numW) + " ")
		out = append(out, nums+signStyle.Render(sign+" ")+d.renderLine(lexer, r.text, r.emphasis, bg, emphBg, textW))
	}
	return out
}

func (d *diffRenderer) sideBySide(rows []diffRow, lexer chroma.Lexer, numW, width int) []string {
	colW := (width - 1) / 2
	textW := max(5, colW-numW-3)
	half := func(r *diffRow, old bool) string {
		if r == nil {
			return strings.Repeat(" ", colW)
		}
		var bg, emphBg, sign string
		signStyle := d.st.lineNum
		no := r.newNo
		if old {
			no = r.oldNo
		}
		switch r.kind {
		case events.LineAdded:
			sign, bg, emphBg = "+", d.theme.DiffAddBg, d.theme.DiffAddEmph
			signStyle = lipgloss.NewStyle().Foreground(c(d.theme.DiffAddSign)).Background(c(bg))
		case events.LineRemoved:
			sign, bg, emphBg = "-", d.theme.DiffDelBg, d.theme.DiffDelEmph
			signStyle = lipgloss.NewStyle().Foreground(c(d.theme.DiffDelSign)).Background(c(bg))
		default:
			sign = " "
		}
		return d.st.lineNum.Render(d.num(no, numW)+" ") + signStyle.Render(sign+" ") + d.renderLine(lexer, r.text, r.emphasis, bg, emphBg, textW)
	}
	sep := d.st.border.Render("│")
	var out []string
	for i := 0; i < len(rows); {
		r := rows[i]
		if r.kind == events.LineContext {
			out = append(out, half(&rows[i], true)+sep+half(&rows[i], false))
			i++
			continue
		}
		var removed, added []*diffRow
		for i < len(rows) && rows[i].kind == events.LineRemoved {
			removed = append(removed, &rows[i])
			i++
		}
		for i < len(rows) && rows[i].kind == events.LineAdded {
			added = append(added, &rows[i])
			i++
		}
		for k := 0; k < max(len(removed), len(added)); k++ {
			var l, rr *diffRow
			if k < len(removed) {
				l = removed[k]
			}
			if k < len(added) {
				rr = added[k]
			}
			out = append(out, half(l, true)+sep+half(rr, false))
		}
	}
	return out
}

// diffStat summarizes a diff as "+a -r".
func diffStat(d *events.Diff) string {
	if d == nil {
		return ""
	}
	if d.NewFile {
		return fmt.Sprintf("new file, %d lines", d.Added)
	}
	return fmt.Sprintf("+%d -%d", d.Added, d.Removed)
}
