package tools

import (
	"strings"

	"github.com/aymanbagabas/go-udiff"

	"github.com/NDari/rho/core/events"
)

// MakeDiff computes a structured unified diff between two texts.
func MakeDiff(before, after string, context int) *events.Diff {
	d := &events.Diff{NewFile: before == "" && after != ""}
	edits := udiff.Lines(before, after)
	if len(edits) == 0 {
		return d
	}
	u, err := udiff.ToUnifiedDiff("a", "b", before, edits, context)
	if err != nil {
		return d
	}
	for _, h := range u.Hunks {
		hk := events.Hunk{OldStart: h.FromLine, NewStart: h.ToLine}
		for _, l := range h.Lines {
			var kind rune
			switch l.Kind {
			case udiff.Insert:
				kind = events.LineAdded
				d.Added++
			case udiff.Delete:
				kind = events.LineRemoved
				d.Removed++
			default:
				kind = events.LineContext
			}
			hk.Lines = append(hk.Lines, events.DiffLine{Kind: kind, Text: strings.TrimSuffix(l.Content, "\n")})
		}
		d.Hunks = append(d.Hunks, hk)
	}
	return d
}
