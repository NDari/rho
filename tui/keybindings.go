package tui

import "strings"

// keyGroup is a set of bindings that apply in one place.
type keyGroup struct {
	where string
	keys  [][2]string // key, what it does
}

// keyBindings lists the TUI's fixed key bindings, for /keybindings. Keep
// in sync with handleKey, handleSearchKey, picker.update and the dialogs.
var keyBindings = []keyGroup{
	{"Editor", [][2]string{
		{"enter", "send the message"},
		{"shift+enter, alt+enter, ctrl+j", "new line"},
		{"↑ / ↓ on the first / last line", "previous / next prompt from history"},
		{"tab", "accept the completion"},
		{"ctrl+g", "edit the input in $VISUAL or $EDITOR"},
		{"ctrl+c", "clear the input; press twice to quit"},
		{"ctrl+d", "quit (on empty input)"},
	}},
	{"While the agent works", [][2]string{
		{"enter", "steer: interrupt after the current tool"},
		{"alt+enter", "queue a follow-up message"},
		{"esc, ctrl+c", "cancel the turn"},
	}},
	{"Transcript", [][2]string{
		{"ctrl+o", "expand or collapse tool output and diffs"},
		{"ctrl+t", "show or hide thinking"},
		{"ctrl+f", "search the transcript"},
		{"pgup / pgdown", "scroll a page"},
		{"shift+↑ / shift+↓, mouse wheel", "scroll"},
		{"ctrl+home / ctrl+end", "jump to the top / follow new output"},
		{"drag", "select text and copy it (tui.mouse_select)"},
		{"shift+drag", "the terminal's own selection"},
	}},
	{"Completion menu", [][2]string{
		{"↑ / ↓, ctrl+p / ctrl+n", "move"},
		{"tab, enter", "accept (enter runs a command)"},
		{"esc", "close"},
	}},
	{"Search", [][2]string{
		{"type", "search text"},
		{"enter, ↓, ctrl+n", "next match"},
		{"↑, shift+enter, ctrl+p", "previous match"},
		{"esc, ctrl+c", "close"},
	}},
	{"Lists (/model, /resume, /config, ...)", [][2]string{
		{"↑ / ↓, ctrl+p / ctrl+n", "move"},
		{"pgup / pgdown, home / end", "move faster"},
		{"type", "filter"},
		{"enter", "select (in /config and /extensions: change)"},
		{"tab", "details (/extensions)"},
		{"ctrl+l", "label the entry (/tree)"},
		{"esc, ctrl+c", "close"},
	}},
	{"Questions from extensions", [][2]string{
		{"y, enter / n, esc", "yes / no"},
		{"enter / esc", "submit / cancel an answer"},
	}},
}

func keyBindingsText() string {
	var b strings.Builder
	for i, g := range keyBindings {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("**" + g.where + "**\n\n| Key | Action |\n|---|---|\n")
		for _, k := range g.keys {
			b.WriteString("| " + k[0] + " | " + k[1] + " |\n")
		}
	}
	b.WriteString("\nKey bindings can't be changed yet.\n")
	return b.String()
}
