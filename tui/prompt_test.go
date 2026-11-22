package tui

import (
	"context"
	"testing"
	"time"

	"github.com/NDari/rho/core/events"
)

// nextUIRequest waits for the next request the UI sends the model.
func nextUIRequest(t *testing.T, m *Model) uiRequestMsg {
	t.Helper()
	select {
	case msg := <-m.uiReqs:
		return msg.(uiRequestMsg)
	case <-time.After(2 * time.Second):
		t.Fatal("no UI request")
		return uiRequestMsg{}
	}
}

// TestWithdrawnPromptCloses checks that a prompt whose asker stopped
// waiting leaves the screen with a notice saying why, and that a stale
// close never removes a newer prompt.
func TestWithdrawnPromptCloses(t *testing.T) {
	m, _ := newTestModel(t)
	ui := UI{m: m}

	ctx, cancel := context.WithCancelCause(context.Background())
	go func() { _, _ = ui.Confirm(ctx, "Allow risky command?", "rm -rf build") }()
	m.handleUIRequest(nextUIRequest(t, m))
	if m.dlg == nil {
		t.Fatal("the prompt did not open")
	}
	cancel(&events.Cancelled{By: events.ByRPCClient})
	m.handleUIRequest(nextUIRequest(t, m))
	if m.dlg != nil {
		t.Error("the withdrawn prompt is still on screen")
	}
	last, _ := m.items[len(m.items)-1].(*noticeItem)
	if last == nil || last.text != `Prompt "Allow risky command?" closed: cancelled by the RPC client` {
		t.Errorf("notice = %+v", last)
	}

	// With a select and a newer confirm on screen, withdrawing the select
	// closes only the select.
	ctx1, cancel1 := context.WithCancelCause(context.Background())
	go func() { _, _ = ui.Select(ctx1, "Pick one", []string{"a", "b"}) }()
	m.handleUIRequest(nextUIRequest(t, m))
	if m.picker == nil {
		t.Fatal("the select did not open")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _, _ = ui.Confirm(ctx2, "Second?", "") }()
	m.handleUIRequest(nextUIRequest(t, m))
	cancel1(&events.Cancelled{By: "extension guard"})
	m.handleUIRequest(nextUIRequest(t, m))
	if m.picker != nil {
		t.Error("the withdrawn select is still on screen")
	}
	if m.dlg == nil || m.dlg.title != "Second?" {
		t.Errorf("closing the select removed the newer prompt: %+v", m.dlg)
	}
	last, _ = m.items[len(m.items)-1].(*noticeItem)
	if last == nil || last.text != `Prompt "Pick one" closed: cancelled by extension guard` {
		t.Errorf("notice = %+v", last)
	}

	// A close for a prompt that was already answered changes nothing.
	m.closePrompt(1, "late")
	if m.dlg == nil {
		t.Error("a stale close removed a prompt")
	}
}
