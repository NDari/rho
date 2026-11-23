package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NDari/rho/core/events"
)

// uiRequestMsg asks the TUI to show a prompt on behalf of an extension.
type uiRequestMsg struct {
	kind    string // confirm, select, input, notify, status, or close
	id      int64
	title   string
	message string // for close, why the prompt was closed
	options []string
	key     string
	reply   chan uiReply
	// done is closed when the asker stops waiting for the answer.
	done <-chan struct{}
}

// openPrompt is an extension prompt on screen, so a close request closes
// that prompt and not a later one.
type openPrompt struct {
	title  string
	dlg    *dialog
	picker *picker
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
	if r.kind == "close" {
		m.closePrompt(r.id, r.message)
		return
	}
	select {
	case <-r.done:
		return // withdrawn before it was shown
	default:
	}
	send := func(v string, ok bool) {
		delete(m.prompts, r.id)
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
		m.prompts[r.id] = openPrompt{title: r.title, dlg: m.dlg}
	case "input":
		ti := textinput.New()
		ti.Placeholder = r.message
		ti.Focus()
		ti.SetWidth(max(10, m.width-6))
		m.dlg = &dialog{kind: "input", title: r.title, input: ti, respond: func(v string, ok bool) tea.Cmd { send(v, ok); return nil }}
		m.prompts[r.id] = openPrompt{title: r.title, dlg: m.dlg}
	case "select":
		var items []pickerItem
		for _, o := range r.options {
			items = append(items, pickerItem{title: o, value: o})
		}
		p := newPicker(r.title, items, func(it pickerItem) tea.Cmd { send(it.value.(string), true); return nil })
		m.picker = p
		// Closing the picker without a choice must still answer.
		m.pendingSelect = func() { send("", false) }
		m.prompts[r.id] = openPrompt{title: r.title, picker: p}
	}
	m.dirty = true
}

// closePrompt removes the extension prompt with the given id, if it is
// still on screen, when its asker stopped waiting, and says why.
func (m *Model) closePrompt(id int64, reason string) {
	p, ok := m.prompts[id]
	if !ok {
		return
	}
	delete(m.prompts, id)
	switch {
	case p.dlg != nil && m.dlg == p.dlg:
		m.dlg = nil
	case p.picker != nil && m.picker == p.picker:
		m.picker = nil
		m.pendingSelect = nil
	default:
		return
	}
	m.notice("warn", fmt.Sprintf("Prompt %q closed: %s", p.title, reason))
	m.dirty = true
}

// UI implements the extension UI interface by forwarding requests to the
// Bubble Tea program.
type UI struct {
	m *Model
}

func (u UI) ask(ctx context.Context, r uiRequestMsg) (uiReply, error) {
	r.id = u.m.uiSeq.Add(1)
	r.reply = make(chan uiReply, 1)
	r.done = ctx.Done()
	select {
	case u.m.uiReqs <- r:
	case <-ctx.Done():
		return uiReply{}, ctx.Err()
	}
	select {
	case rep := <-r.reply:
		return rep, nil
	case <-ctx.Done():
		// Take the prompt off the screen, saying why.
		closeMsg := uiRequestMsg{kind: "close", id: r.id, message: closeReason(ctx)}
		go func() {
			select {
			case u.m.uiReqs <- closeMsg:
			case <-time.After(5 * time.Second): // the TUI has quit
			}
		}()
		return uiReply{}, ctx.Err()
	}
}

// closeReason says why ctx ended, for a prompt that was closed. A cancel
// that records who did it (events.Cancelled, which also matches
// context.Canceled) and any other cause give their text; a bare cancel
// does not say why.
func closeReason(ctx context.Context) string {
	cause := context.Cause(ctx)
	var c *events.Cancelled
	switch {
	case errors.As(cause, &c):
		return c.Error()
	case cause != nil && !errors.Is(cause, context.Canceled):
		return cause.Error()
	}
	return "no longer needed"
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
