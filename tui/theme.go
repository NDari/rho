package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/NDari/rho/core/theme"
)

// Theme is a color theme; see core/theme.
type Theme = theme.Theme

// c converts a theme color string to a color.
func c(s string) color.Color {
	if s == "" {
		return nil
	}
	return lipgloss.Color(s)
}

// styles are lipgloss styles derived from a theme.
type styles struct {
	text, muted, subtle, accent, errorS, warning, success lipgloss.Style
	toolName, thinking, lineNum, border                   lipgloss.Style
	user, status, statusKey, title, selected, search      lipgloss.Style
}

func newStyles(t Theme) styles {
	s := lipgloss.NewStyle
	return styles{
		text:      s().Foreground(c(t.Text)),
		muted:     s().Foreground(c(t.Muted)),
		subtle:    s().Foreground(c(t.Subtle)),
		accent:    s().Foreground(c(t.Accent)),
		errorS:    s().Foreground(c(t.Error)),
		warning:   s().Foreground(c(t.Warning)),
		success:   s().Foreground(c(t.Success)),
		toolName:  s().Foreground(c(t.ToolName)).Bold(true),
		thinking:  s().Foreground(c(t.Thinking)).Italic(true),
		lineNum:   s().Foreground(c(t.LineNumber)),
		border:    s().Foreground(c(t.Border)),
		user:      s().Background(c(t.UserBg)).Foreground(c(t.Text)),
		status:    s().Background(c(t.StatusBg)).Foreground(c(t.StatusFg)),
		statusKey: s().Background(c(t.StatusBg)).Foreground(c(t.Accent)).Bold(true),
		title:     s().Foreground(c(t.Accent)).Bold(true),
		selected:  s().Background(c(t.UserBg)).Foreground(c(t.Accent)).Bold(true),
		search:    s().Background(c(t.SearchBg)).Foreground(c(t.SearchFg)),
	}
}
