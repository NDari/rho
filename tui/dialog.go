package tui

import (
	"context"
	"errors"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// uiRequestMsg asks the TUI to show a prompt on behalf of an extension.
type uiRequestMsg struct {
	kind    string // confirm, select, input, notify, status
	title   string
	message string
	options []string
	key     string
	reply   chan uiReply
}

type uiReply struct {
	value string
	ok    bool
}

func (m *Model) openInput(title, placeholder, initial string, done func(string) tea.Cmd) {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.SetValue(initial)
	ti.Focus()
	ti.SetWidth(max(10, m.width-6))
	m.dlg = &dialog{kind: "input", title: title, input: ti, respond: func(v string, ok bool) tea.Cmd {
		if ok {
			return done(v)
		}
		return nil
	}}
}

func (m *Model) handleDialogKey(msg tea.KeyPressMsg) tea.Cmd {
	d := m.dlg
	switch d.kind {
	case "confirm":
		switch strings.ToLower(msg.String()) {
		case "y", "enter":
			m.dlg = nil
			return d.respond("yes", true)
		case "n", "esc", "ctrl+c":
			m.dlg = nil
			return d.respond("", false)
		}
		return nil
	default:
		switch msg.String() {
		case "enter":
			m.dlg = nil
			return d.respond(d.input.Value(), true)
		case "esc", "ctrl+c":
			m.dlg = nil
			return d.respond("", false)
		}
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		return cmd
	}
}

func (m *Model) dialogView() []string {
	d := m.dlg
	w := max(10, m.width)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(m.theme.Accent)).Width(w-2).Padding(0, 1)
	var body []string
	body = append(body, m.st.title.Render(d.title))
	if d.message != "" {
		body = append(body, d.message)
	}
	switch d.kind {
	case "confirm":
		body = append(body, m.st.muted.Render("y / enter: yes · n / esc: no"))
	default:
		body = append(body, d.input.View(), m.st.muted.Render("enter: ok · esc: cancel"))
	}
	return strings.Split(box.Render(strings.Join(body, "\n")), "\n")
}

func (m *Model) handleUIRequest(r uiRequestMsg) {
	send := func(v string, ok bool) {
		if r.reply != nil {
			r.reply <- uiReply{value: v, ok: ok}
		}
	}
	switch r.kind {
	case "notify":
		level := r.key
		if level == "" {
			level = "info"
		}
		m.notice(level, r.message)
		send("", true)
		return
	case "status":
		if r.message == "" {
			delete(m.statuses, r.key)
		} else {
			m.statuses[r.key] = r.message
		}
		send("", true)
		return
	case "confirm":
		m.dlg = &dialog{kind: "confirm", title: r.title, message: r.message, respond: func(v string, ok bool) tea.Cmd { send(v, ok); return nil }}
	case "input":
		ti := textinput.New()
		ti.Placeholder = r.message
		ti.Focus()
		ti.SetWidth(max(10, m.width-6))
		m.dlg = &dialog{kind: "input", title: r.title, input: ti, respond: func(v string, ok bool) tea.Cmd { send(v, ok); return nil }}
	case "select":
		var items []pickerItem
		for _, o := range r.options {
			items = append(items, pickerItem{title: o, value: o})
		}
		p := newPicker(r.title, items, func(it pickerItem) tea.Cmd { send(it.value.(string), true); return nil })
		m.picker = p
		// Closing the picker without a choice must still answer.
		m.pendingSelect = func() { send("", false) }
	}
	m.dirty = true
}

// UI implements the extension UI interface by forwarding requests to the
// Bubble Tea program.
type UI struct {
	m *Model
}

func (u UI) ask(ctx context.Context, r uiRequestMsg) (uiReply, error) {
	r.reply = make(chan uiReply, 1)
	select {
	case u.m.uiReqs <- r:
	case <-ctx.Done():
		return uiReply{}, ctx.Err()
	}
	select {
	case rep := <-r.reply:
		return rep, nil
	case <-ctx.Done():
		return uiReply{}, ctx.Err()
	}
}

// ErrDismissed is returned when the user dismisses a prompt.
var ErrDismissed = errors.New("dismissed by the user")

// Confirm asks a yes/no question.
func (u UI) Confirm(ctx context.Context, title, message string) (bool, error) {
	rep, err := u.ask(ctx, uiRequestMsg{kind: "confirm", title: title, message: message})
	return rep.ok, err
}

// Select asks the user to pick one option.
func (u UI) Select(ctx context.Context, title string, options []string) (string, error) {
	rep, err := u.ask(ctx, uiRequestMsg{kind: "select", title: title, options: options})
	if err == nil && !rep.ok {
		err = ErrDismissed
	}
	return rep.value, err
}

// Input asks for a line of text.
func (u UI) Input(ctx context.Context, title, placeholder string) (string, error) {
	rep, err := u.ask(ctx, uiRequestMsg{kind: "input", title: title, message: placeholder})
	if err == nil && !rep.ok {
		err = ErrDismissed
	}
	return rep.value, err
}

// Notify shows a message.
func (u UI) Notify(level, text string) {
	_, _ = u.ask(context.Background(), uiRequestMsg{kind: "notify", key: level, message: text})
}

// SetStatus sets (or clears) a status bar entry.
func (u UI) SetStatus(key, text string) {
	_, _ = u.ask(context.Background(), uiRequestMsg{kind: "status", key: key, message: text})
}
