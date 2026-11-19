package tui

import (
	"encoding/json"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/session"
)

// loadTranscript rebuilds the transcript from the session's current path.
func (m *Model) loadTranscript() {
	var warnings []item
	for _, it := range m.items {
		if n, ok := it.(*noticeItem); ok && n.level == "warn" && len(m.tools) == 0 && m.cur == nil {
			warnings = append(warnings, n)
		}
	}
	m.items = warnings
	m.tools = map[string]*toolItem{}
	m.cur = nil
	m.compactIt = nil

	sess := m.agent.Session()
	cv := sess.BuildContext()
	for i, msg := range cv.Messages {
		switch {
		case msg.Meta.Synthetic == session.SyntheticCompaction:
			ci := &compactionItem{}
			if cv.Compaction != nil {
				ci.summary = cv.Compaction.Summary
				ci.before = cv.Compaction.TokensBefore
			}
			m.addItem(ci)
		case msg.Meta.Synthetic == session.SyntheticAsideNote:
			m.addItem(&noticeItem{level: "info", text: msg.TextContent()})
		case msg.Role == provider.RoleUser:
			m.addUserMessage(msg)
		case msg.Role == provider.RoleAssistant:
			a := &assistantItem{text: msg.TextContent(), cancelled: msg.Meta.StopReason == provider.StopCancelled}
			for _, b := range msg.Content {
				if b.Type == provider.BlockThinking {
					a.thinking += b.Text
				}
			}
			if !a.empty() || a.cancelled {
				m.addItem(a)
			}
			for _, t := range toolCallsFromMessage(msg) {
				m.tools[t.id] = t
				m.addItem(t)
			}
		case msg.Role == provider.RoleToolResult:
			var display *events.Display
			if id := cv.EntryIDs[i]; id != "" {
				if e, ok := sess.Get(id); ok && len(e.Display) > 0 {
					var d events.Display
					if json.Unmarshal(e.Display, &d) == nil {
						display = &d
					}
				}
			}
			for _, b := range msg.Content {
				if b.Type != provider.BlockToolResult {
					continue
				}
				if t := m.tools[b.CallID]; t != nil {
					t.result = blockText(b)
					t.isError = b.IsError
					t.display = display
				}
			}
		}
	}
	if cv.Aside {
		m.addItem(&noticeItem{level: "warn", text: "side conversation: read-only tools; nothing here joins the main conversation. /back to return."})
	}
	m.usage = events.Usage{
		ContextTokens: m.agent.EstimateContext(cv),
		ContextWindow: m.agent.Model().ContextWindow,
		SessionCost:   m.agent.Cost(),
	}
	m.follow = true
	m.dirty = true
}
