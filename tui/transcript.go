package tui

import (
	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/events"
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

	t := m.app.Transcript()
	for _, it := range t.Items {
		switch it.Kind {
		case app.ItemCompaction:
			m.addItem(&compactionItem{summary: it.Summary, before: it.TokensBefore})
		case app.ItemNote:
			m.addItem(&noticeItem{level: "info", text: it.Text})
		case app.ItemUser:
			m.addItem(&userItem{text: userText(it.Text, it.Attached)})
		case app.ItemAssistant:
			m.addItem(&assistantItem{text: it.Text, thinking: it.Thinking, cancelled: it.Cancelled})
		case app.ItemTool:
			tc := it.Tool
			ti := &toolItem{id: tc.ID, name: tc.Name, args: tc.Args, result: tc.Result, isError: tc.IsError, display: tc.Display}
			m.tools[tc.ID] = ti
			m.addItem(ti)
		}
	}
	if t.Aside {
		m.addItem(&noticeItem{level: "warn", text: "side conversation: read-only tools; nothing here joins the main conversation. /back to return."})
	}
	for _, it := range m.postReload {
		m.addItem(it)
	}
	m.postReload = nil
	cv := m.agent.Session().BuildContext()
	m.usage = events.Usage{
		ContextTokens: m.agent.EstimateContext(cv),
		ContextWindow: m.agent.Model().ContextWindow,
		SessionCost:   m.agent.Cost(),
	}
	m.follow = true
	m.dirty = true
}
