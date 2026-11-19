package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/NDari/rho/core/compact"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/session"
)

// StartAside begins a side conversation from the current main position.
func (a *Agent) StartAside() error {
	if a.Busy() {
		return ErrBusy
	}
	sess := a.Session()
	if sess.BuildContext().Aside {
		return errors.New("already in a side conversation; use /back first")
	}
	if _, err := sess.Append(session.Entry{Type: session.TypeAsideStart}); err != nil {
		return err
	}
	a.emitSessionChanged()
	return nil
}

// EndAside returns to the main conversation, optionally carrying a note.
func (a *Agent) EndAside(note string) error {
	if a.Busy() {
		return ErrBusy
	}
	sess := a.Session()
	cv := sess.BuildContext()
	if !cv.Aside {
		return errors.New("not in a side conversation")
	}
	if err := sess.Checkout(cv.MainLeaf); err != nil {
		return err
	}
	if note = strings.TrimSpace(note); note != "" {
		if _, err := sess.Append(session.Entry{Type: session.TypeAsideSummary, Note: note}); err != nil {
			return err
		}
	}
	a.emitSessionChanged()
	return nil
}

// SummarizeAside asks the model for a short note about the current side
// conversation, suitable for EndAside.
func (a *Agent) SummarizeAside(ctx context.Context) (string, error) {
	sess := a.Session()
	cv := sess.BuildContext()
	if !cv.Aside {
		return "", errors.New("not in a side conversation")
	}
	main := sess.ContextAt(cv.MainLeaf)
	asideMsgs := cv.Messages[len(main.Messages):]
	if len(asideMsgs) == 0 {
		return "", errors.New("the side conversation is empty")
	}
	model := a.Model()
	prov, err := a.providers.Get(model.Provider)
	if err != nil {
		return "", err
	}
	s, usage, err := compact.Summarize(ctx, prov, model, asideMsgs,
		"Write a short note (a few sentences or bullets) capturing the conclusions of this side conversation that matter for the main task. Output only the note.")
	a.addCost(usage)
	return s, err
}

// Checkout moves the current position to an entry ("" = the start).
func (a *Agent) Checkout(id string) error {
	if a.Busy() {
		return ErrBusy
	}
	if err := a.Session().Checkout(id); err != nil {
		return err
	}
	a.adoptSessionModel()
	a.emitSessionChanged()
	return nil
}

// RewindTo checks out the parent of a user message, so the conversation
// continues as if that message had not been sent. It returns the message
// text so a frontend can put it back in the editor.
func (a *Agent) RewindTo(id string) (string, error) {
	e, ok := a.Session().Get(id)
	if !ok {
		return "", session.ErrNoEntry
	}
	if e.Type != session.TypeMessage || e.Message == nil || e.Message.Role != provider.RoleUser {
		return "", errors.New("can only rewind to a user message")
	}
	if err := a.Checkout(e.Parent); err != nil {
		return "", err
	}
	return e.Message.TextContent(), nil
}

// Label sets (or clears, with "") a label on an entry.
func (a *Agent) Label(id, label string) error {
	_, err := a.Session().Append(session.Entry{Type: session.TypeLabel, Target: id, Label: label})
	return err
}
