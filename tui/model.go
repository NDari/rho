// Package tui is Rho's interactive terminal frontend.
package tui

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/NDari/rho/core/agent"
	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
)

const frameInterval = 33 * time.Millisecond

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// eventQueue buffers agent events between frames.
type eventQueue struct {
	mu     sync.Mutex
	events []events.Event
}

func (q *eventQueue) push(e events.Event) {
	q.mu.Lock()
	q.events = append(q.events, e)
	q.mu.Unlock()
}

func (q *eventQueue) drain() []events.Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.events
	q.events = nil
	return out
}

type tickMsg time.Time

// asyncResultMsg delivers the result of background work (compaction, etc).
type asyncResultMsg struct {
	err  error
	done func(m *Model) tea.Cmd
}

// dialog is a modal prompt (confirm or text input).
type dialog struct {
	kind    string // "confirm" or "input"
	title   string
	message string
	input   textinput.Model
	respond func(value string, ok bool) tea.Cmd
}

// Model is the Bubble Tea model.
type Model struct {
	app   *app.App
	agent *agent.Agent

	themes map[string]Theme
	theme  Theme
	st     styles
	md     *markdown
	hl     *highlighter
	diffR  *diffRenderer

	width, height int

	items     []item
	tools     map[string]*toolItem
	cur       *assistantItem
	compactIt *compactionItem
	lines     []string
	dirty     bool
	offset    int
	follow    bool

	input      textarea.Model
	hist       *history
	comp       *completion
	picker     *picker
	dlg        *dialog
	search     *searchState
	quitArmed  time.Time
	flash      string
	flashUntil time.Time
	sel        *selection

	busy          bool
	frame         int
	queue         events.Queue
	usage         events.Usage
	expandAll     bool
	showThinking  bool
	statuses      map[string]string
	gitBranch     string
	events        *eventQueue
	uiReqs        chan tea.Msg
	quitting      bool
	ext           ExtensionCommands
	pendingSelect func()
	// postReload items are shown after the next transcript reload (for
	// notices about the session change that causes the reload).
	postReload []item
}

type searchState struct {
	query   string
	matches []int
	idx     int
}

// New creates the TUI model.
func New(a *app.App) *Model {
	m := &Model{
		app:      a,
		agent:    a.Agent,
		tools:    map[string]*toolItem{},
		follow:   true,
		hist:     loadHistory(a.Paths.Home),
		events:   &eventQueue{},
		statuses: map[string]string{},
		uiReqs:   make(chan tea.Msg, 16),
	}
	themes, warnings := LoadThemes(a.Paths.Home)
	m.themes = themes
	t, w := pickTheme(themes, a.Config.Theme)
	if w != "" {
		warnings = append(warnings, w)
	}
	m.setTheme(t)

	m.input = newInput()
	m.applyInputStyles()
	m.input.Focus()

	a.Agent.SetSink(m.events.push)
	for _, w := range append(a.Warnings, warnings...) {
		m.addItem(&noticeItem{level: "warn", text: w})
	}
	m.showThinking = a.Config.TUI.ShowThinking
	m.gitBranch = app.GitBranch(a.Paths.ProjectRoot)
	m.loadTranscript()
	return m
}

func (m *Model) setTheme(t Theme) {
	m.theme = t
	m.st = newStyles(t)
	m.md = newMarkdown(t)
	m.hl = newHighlighter(t.Chroma)
	m.diffR = &diffRenderer{theme: t, hl: m.hl, st: m.st}
	m.dirty = true
}

func (m *Model) applyInputStyles() {
	s := textarea.DefaultStyles(m.theme.IsDark())
	for _, st := range []*textarea.StyleState{&s.Focused, &s.Blurred} {
		st.CursorLine = lipgloss.NewStyle()
		st.Text = lipgloss.NewStyle().Foreground(c(m.theme.Text))
		st.Placeholder = lipgloss.NewStyle().Foreground(c(m.theme.Muted))
		st.EndOfBuffer = lipgloss.NewStyle()
	}
	s.Cursor.Color = c(m.theme.Accent)
	m.input.SetStyles(s)
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.tick(), m.waitUIRequest())
}

func (m *Model) tick() tea.Cmd {
	return tea.Tick(frameInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) waitUIRequest() tea.Cmd {
	return func() tea.Msg { return <-m.uiReqs }
}

func (m *Model) addItem(it item) {
	m.items = append(m.items, it)
	m.dirty = true
}

func (m *Model) setFlash(s string) {
	m.flash = s
	m.flashUntil = time.Now().Add(4 * time.Second)
}

// renderCtx builds the context for rendering items.
func (m *Model) renderCtx() *renderCtx {
	rc := &renderCtx{
		width:        max(20, m.width-1),
		theme:        m.theme,
		st:           m.st,
		md:           m.md,
		hl:           m.hl,
		diff:         m.diffR,
		expandAll:    m.expandAll,
		showThinking: m.showThinking,
		sideBySide:   m.app.Config.TUI.SideBySideWidth,
		spinner:      spinnerFrames[m.frame%len(spinnerFrames)],
		cwd:          m.app.Paths.Cwd,
	}
	rc.key = fmt.Sprintf("%d|%s|%v|%v", rc.width, m.theme.Name, m.expandAll, m.showThinking)
	return rc
}

func (m *Model) rebuildLines() {
	rc := m.renderCtx()
	var lines []string
	for i, it := range m.items {
		r := it.render(rc)
		if len(r) == 0 {
			continue
		}
		if i > 0 && len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, r...)
	}
	m.lines = lines
	m.dirty = false
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(10, m.width-2))
		m.dirty = true
		return m, nil

	case tickMsg:
		m.frame++
		for _, e := range m.events.drain() {
			m.handleEvent(e)
		}
		if m.busy || m.compactIt != nil && m.compactIt.running {
			m.dirty = true // animate spinners
		}
		if m.quitting {
			return m, tea.Quit
		}
		if !m.busy && !m.dirty {
			// Idle: poll less often to save CPU.
			return m, tea.Tick(4*frameInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
		}
		return m, m.tick()

	case asyncResultMsg:
		if msg.err != nil {
			m.addItem(&noticeItem{level: "error", text: msg.err.Error()})
		}
		if msg.done != nil {
			return m, msg.done(m)
		}
		return m, nil

	case uiRequestMsg:
		m.handleUIRequest(msg)
		return m, m.waitUIRequest()

	case tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		return m, m.handleMouse(msg.(tea.MouseMsg))

	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scroll(-3)
		case tea.MouseWheelDown:
			m.scroll(3)
		}
		return m, nil

	case tea.PasteMsg:
		if m.dlg != nil {
			var cmd tea.Cmd
			m.dlg.input, cmd = m.dlg.input.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.afterInputChange()
		return m, cmd

	case editorDoneMsg:
		m.finishEditor(msg)
		return m, nil

	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) transcriptHeight() int {
	h := m.height - m.inputHeight() - 1 // status bar
	if q := m.queueLines(); len(q) > 0 {
		h -= len(q)
	}
	if m.comp != nil {
		h -= min(len(m.comp.items), 8)
	}
	return max(1, h)
}

func (m *Model) scroll(d int) {
	if m.dirty {
		m.rebuildLines()
	}
	maxOff := max(0, len(m.lines)-m.transcriptHeight())
	if m.follow {
		m.offset = maxOff
	}
	m.offset = min(max(0, m.offset+d), maxOff)
	m.follow = m.offset >= maxOff
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	m.sel = nil

	if m.dlg != nil {
		return m.handleDialogKey(msg)
	}
	if m.picker != nil {
		p := m.picker
		cmd, done := p.update(msg)
		if done {
			if m.picker == p {
				m.picker = nil
			}
			if m.pendingSelect != nil {
				if k := msg.String(); k != "enter" {
					m.pendingSelect()
				}
				m.pendingSelect = nil
			}
			m.dirty = true
		}
		return cmd
	}
	if m.search != nil {
		return m.handleSearchKey(msg)
	}

	switch key {
	case "ctrl+c":
		if m.busy {
			m.agent.Cancel()
			return nil
		}
		if m.input.Value() != "" {
			m.input.Reset()
			m.afterInputChange()
			return nil
		}
		if time.Since(m.quitArmed) < 2*time.Second {
			return m.quit()
		}
		m.quitArmed = time.Now()
		m.setFlash("press ctrl+c again to quit")
		return nil
	case "ctrl+d":
		if m.input.Value() == "" {
			return m.quit()
		}
	case "esc":
		if m.comp != nil {
			m.comp = nil
			return nil
		}
		if m.busy {
			m.agent.Cancel()
		}
		return nil
	case "ctrl+o":
		m.expandAll = !m.expandAll
		m.dirty = true
		return nil
	case "ctrl+t":
		m.showThinking = !m.showThinking
		m.dirty = true
		return nil
	case "ctrl+f":
		m.search = &searchState{}
		return nil
	case "ctrl+g":
		m.comp = nil
		return m.openEditor()
	case "pgup":
		m.scroll(-m.transcriptHeight() + 2)
		return nil
	case "pgdown":
		m.scroll(m.transcriptHeight() - 2)
		return nil
	case "shift+up":
		m.scroll(-1)
		return nil
	case "shift+down":
		m.scroll(1)
		return nil
	case "ctrl+end":
		m.follow = true
		m.dirty = true
		return nil
	case "ctrl+home":
		m.follow = false
		m.offset = 0
		return nil
	}

	if m.comp != nil {
		switch key {
		case "up", "ctrl+p":
			m.comp.move(-1)
			return nil
		case "down", "ctrl+n":
			m.comp.move(1)
			return nil
		case "tab", "enter":
			m.acceptCompletion()
			if key == "enter" && m.comp == nil && strings.HasPrefix(m.input.Value(), "/") && !strings.Contains(strings.TrimSpace(m.input.Value()), " ") {
				// Enter on a command completion runs it right away.
				return m.submit(false)
			}
			return nil
		}
	}

	switch key {
	case "enter":
		return m.submit(false)
	case "alt+enter":
		if m.busy {
			return m.submit(true)
		}
		m.input.InsertString("\n")
		m.afterInputChange()
		return nil
	case "shift+enter", "ctrl+j":
		m.input.InsertString("\n")
		m.afterInputChange()
		return nil
	case "up":
		if m.input.Line() == 0 {
			if s, ok := m.hist.prev(m.input.Value()); ok {
				m.input.SetValue(s)
				m.afterInputChange()
				return nil
			}
		}
	case "down":
		if m.input.Line() == m.input.LineCount()-1 {
			if s, ok := m.hist.next(); ok {
				m.input.SetValue(s)
				m.afterInputChange()
				return nil
			}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.afterInputChange()
	return cmd
}

func (m *Model) quit() tea.Cmd {
	m.quitting = true
	m.agent.Cancel()
	return tea.Quit
}

// newInput creates the chat input box.
func newInput() textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Placeholder = "Message Rho · / for commands · @ for files"
	ta.CharLimit = 0
	// The textarea sizes itself from its own soft-wrapped line count, so
	// the box never lags behind the wrap and scrolls the first line away.
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = 10
	ta.KeyMap.InsertNewline.SetEnabled(false)
	ta.SetHeight(1)
	return ta
}

func (m *Model) afterInputChange() {
	m.updateCompletion()
}

func (m *Model) inputContentHeight() int { return m.input.Height() }

// inputHeight counts the input box, its borders and the blank line above
// and below it.
func (m *Model) inputHeight() int { return m.inputContentHeight() + 4 }

func (m *Model) updateCompletion() {
	val := m.input.Value()
	lines := strings.Split(val, "\n")
	row := m.input.Line()
	if row >= len(lines) {
		m.comp = nil
		return
	}
	line := []rune(lines[row])
	col := min(m.input.Column(), len(line))
	before := string(line[:col])
	m.comp = computeCompletion(before, row == 0, m.commandList(), m.app.Paths.Cwd)
}

func (m *Model) acceptCompletion() {
	if m.comp == nil || len(m.comp.items) == 0 {
		return
	}
	it := m.comp.items[m.comp.cursor]
	val := m.input.Value()
	repl, _ := it.value.(string)
	if i := strings.LastIndex(val, m.comp.token); i >= 0 {
		val = val[:i] + repl + val[i+len(m.comp.token):]
	}
	m.input.SetValue(val)
	m.comp = nil
	if strings.HasSuffix(repl, "/") {
		m.updateCompletion() // keep completing inside directories
	}
}

func (m *Model) submit(followUp bool) tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}
	m.hist.add(text)
	m.input.Reset()
	m.afterInputChange()
	m.follow = true
	m.dirty = true

	if strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//") {
		return m.runCommand(text)
	}
	if strings.HasPrefix(text, "//") {
		text = text[1:]
	}
	return m.sendText(text, followUp)
}

func (m *Model) sendText(text string, followUp bool) tea.Cmd {
	if m.busy {
		var err error
		if followUp {
			err = m.agent.FollowUp(text)
		} else {
			err = m.agent.Steer(text)
		}
		if err != nil {
			m.addItem(&noticeItem{level: "error", text: err.Error()})
		}
		return nil
	}
	if err := m.agent.Prompt(m.app.BuildUserMessage(text)); err != nil {
		m.addItem(&noticeItem{level: "error", text: err.Error()})
	} else {
		m.busy = true
	}
	return nil
}

func (m *Model) handleSearchKey(msg tea.KeyPressMsg) tea.Cmd {
	s := m.search
	switch msg.String() {
	case "esc", "ctrl+c":
		m.search = nil
		m.dirty = true
		return nil
	case "enter", "down", "ctrl+n":
		m.jumpSearch(1)
		return nil
	case "up", "shift+enter", "ctrl+p":
		m.jumpSearch(-1)
		return nil
	case "backspace":
		if s.query != "" {
			r := []rune(s.query)
			s.query = string(r[:len(r)-1])
		}
	default:
		if msg.Text != "" {
			s.query += msg.Text
		}
	}
	m.findMatches()
	if len(s.matches) > 0 {
		s.idx = len(s.matches) - 1
		m.showLine(s.matches[s.idx])
	}
	return nil
}

func (m *Model) findMatches() {
	if m.dirty {
		m.rebuildLines()
	}
	s := m.search
	s.matches = nil
	if s.query == "" {
		return
	}
	q := strings.ToLower(s.query)
	for i, l := range m.lines {
		if strings.Contains(strings.ToLower(stripANSI(l)), q) {
			s.matches = append(s.matches, i)
		}
	}
}

func (m *Model) jumpSearch(d int) {
	s := m.search
	if len(s.matches) == 0 {
		return
	}
	s.idx = (s.idx + d + len(s.matches)) % len(s.matches)
	m.showLine(s.matches[s.idx])
}

func (m *Model) showLine(i int) {
	h := m.transcriptHeight()
	m.follow = false
	m.offset = max(0, min(i-h/2, len(m.lines)-h))
}

// handleEvent applies one agent event to the transcript.
func (m *Model) handleEvent(e events.Event) {
	m.dirty = true
	switch ev := e.(type) {
	case events.TurnStart:
		m.busy = true
	case events.MessageStart:
		if m.cur == nil || !m.cur.empty() {
			if m.cur != nil { // a discarded partial response
				m.cur.endThinking(time.Now())
				m.cur.streaming = false
				m.cur.touch()
			}
			m.cur = &assistantItem{streaming: true}
			m.addItem(m.cur)
		}
		// A retry reuses the empty item and starts the wait again.
		m.cur.requested = time.Now()
	case events.TextDelta:
		if m.cur != nil {
			m.cur.text += ev.Text
			m.cur.composing = ""
			if strings.TrimSpace(ev.Text) != "" {
				m.cur.endThinking(time.Now())
			}
			m.cur.touch()
		}
	case events.ThinkingDelta:
		if m.cur != nil {
			m.cur.thinking += ev.Text
			m.cur.startThinking(time.Now())
			m.cur.touch()
		}
	case events.ToolCallDelta:
		if m.cur != nil && ev.Name != "" {
			m.cur.composing = ev.Name
			m.cur.endThinking(time.Now())
			m.cur.touch()
		}
	case events.MessageEnd:
		switch ev.Message.Role {
		case provider.RoleUser:
			m.addUserMessage(ev.Message)
		case provider.RoleAssistant:
			if m.cur != nil {
				m.cur.text = ev.Message.TextContent()
				m.cur.endThinking(time.Now())
				m.cur.streaming = false
				m.cur.composing = ""
				m.cur.cancelled = ev.Message.Meta.StopReason == provider.StopCancelled
				m.cur.touch()
				m.cur = nil
			}
		}
	case events.ToolStart:
		t := &toolItem{id: ev.ID, name: ev.Name, args: ev.Args, running: true, started: time.Now()}
		m.tools[ev.ID] = t
		m.addItem(t)
	case events.ToolOutput:
		if t := m.tools[ev.ID]; t != nil {
			t.output.WriteString(ev.Text)
			if t.output.Len() > 256*1024 {
				s := t.output.String()
				t.output.Reset()
				t.output.WriteString(s[len(s)-128*1024:])
			}
			t.touch()
		}
	case events.ToolEnd:
		if t := m.tools[ev.ID]; t != nil {
			t.running = false
			t.isError = ev.Result.IsError
			t.result = app.ResultText(ev.Result)
			t.display = ev.Display
			t.duration = ev.Duration
			t.touch()
		}
	case events.Usage:
		m.usage = ev
	case events.Retry:
		m.addItem(&noticeItem{level: "warn", text: fmt.Sprintf("retrying in %s (attempt %d/%d): %s", ev.Delay, ev.Attempt, ev.Max, ev.Error)})
	case events.Error:
		m.addItem(&noticeItem{level: "error", text: ev.Message})
	case events.Notice:
		m.addItem(&noticeItem{level: ev.Level, text: ev.Text})
	case events.CompactionStart:
		m.compactIt = &compactionItem{running: true, reason: ev.Reason, before: ev.Tokens}
		m.addItem(m.compactIt)
	case events.CompactionEnd:
		if m.compactIt != nil {
			m.compactIt.running = false
			m.compactIt.after = ev.TokensAfter
			m.compactIt.err = ev.Error
			if ev.EntryID != "" {
				if e, ok := m.agent.Session().Get(ev.EntryID); ok {
					m.compactIt.summary = e.Summary
				}
			}
			m.compactIt.touch()
			m.compactIt = nil
		}
	case events.Status:
		if ev.Text == "" {
			delete(m.statuses, ev.Key)
		} else {
			m.statuses[ev.Key] = ev.Text
		}
	case events.Queue:
		m.queue = ev
	case events.ModelChanged:
		m.addItem(&noticeItem{level: "info", text: "model: " + ev.Model})
	case events.SessionChanged:
		m.loadTranscript()
	case events.TurnEnd:
		m.busy = false
		if m.cur != nil {
			m.cur.endThinking(time.Now())
			m.cur.streaming = false
			m.cur.touch()
			m.cur = nil
		}
		for _, t := range m.tools {
			if t.running {
				t.running = false
				t.isError = true
				t.touch()
			}
		}
		if ev.Reason == "cancelled" {
			m.addItem(&noticeItem{level: "warn", text: "cancelled"})
		}
		left := m.agent.TakeQueue()
		if ev.Unanswered != "" {
			// The prompt got no reply and was taken off the conversation:
			// drop it from the transcript and put it back in the editor, so
			// sending again (edited or not) replaces it.
			m.dropLastUserItem()
			text := ev.Unanswered
			if strings.HasPrefix(text, "/") {
				text = "/" + text // sent as //..., not a command
			}
			left = append([]string{text}, left...)
			if v := m.input.Value(); v != "" {
				left = append(left, v)
			}
			m.input.SetValue(strings.Join(left, "\n\n"))
			m.afterInputChange()
		} else if len(left) > 0 && m.input.Value() == "" {
			m.input.SetValue(strings.Join(left, "\n\n"))
			m.afterInputChange()
		}
		m.gitBranch = app.GitBranch(m.app.Paths.ProjectRoot)
	}
}

// dropLastUserItem removes the most recent user message from the
// transcript.
func (m *Model) dropLastUserItem() {
	for i := len(m.items) - 1; i >= 0; i-- {
		if _, ok := m.items[i].(*userItem); ok {
			m.items = slices.Delete(m.items, i, i+1)
			m.dirty = true
			return
		}
	}
}

func (m *Model) addUserMessage(msg provider.Message) {
	m.addItem(&userItem{text: userText(app.UserMessageText(msg))})
}

// userText shows a user message with its attached files.
func userText(text string, attached []string) string {
	if len(attached) > 0 {
		text += "\n(attached: " + strings.Join(attached, ", ") + ")"
	}
	return text
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "rho · " + filepath.Base(m.app.Paths.Cwd)
	if m.width == 0 {
		return v
	}
	if m.dirty {
		m.rebuildLines()
	}
	th := m.transcriptHeight()

	var body []string
	if m.picker != nil {
		body = m.picker.render(m.renderCtx(), th)
	} else {
		maxOff := max(0, len(m.lines)-th)
		if m.follow {
			m.offset = maxOff
		}
		m.offset = min(m.offset, maxOff)
		end := min(len(m.lines), m.offset+th)
		body = append(body, m.lines[m.offset:end]...)
		body = m.highlightSelection(body, m.offset)
		if m.search != nil && m.search.query != "" {
			body = m.highlightSearch(body, m.offset)
		}
		for len(body) < th {
			body = append(body, "")
		}
		if !m.follow && len(body) > 0 {
			hint := m.st.subtle.Render(fmt.Sprintf("↓ %d more lines · ctrl+end to follow", len(m.lines)-end))
			body[len(body)-1] = hint
		}
	}

	var parts []string
	parts = append(parts, body...)
	parts = append(parts, m.queueLines()...)
	if m.comp != nil {
		parts = append(parts, m.comp.render(m.renderCtx(), 8)...)
	}
	parts = append(parts, "")
	parts = append(parts, m.inputView()...)
	parts = append(parts, "", m.statusLine())
	v.SetContent(strings.Join(parts, "\n"))
	if m.dlg == nil && m.picker == nil && m.search == nil {
		if cur := m.input.Cursor(); cur != nil {
			// Below the input: its bottom border, a blank line and the status bar.
			cur.Y += len(parts) - m.inputContentHeight() - 3
			cur.X++
			v.Cursor = cur
		}
	}
	return v
}

func (m *Model) highlightSearch(lines []string, offset int) []string {
	q := m.search.query
	out := make([]string, len(lines))
	for i, l := range lines {
		plain := stripANSI(l)
		idx := strings.Index(strings.ToLower(plain), strings.ToLower(q))
		if idx < 0 {
			out[i] = l
			continue
		}
		st := m.st.search
		if len(m.search.matches) > 0 && m.search.matches[m.search.idx] == offset+i {
			st = st.Bold(true).Underline(true)
		}
		out[i] = plain[:idx] + st.Render(plain[idx:idx+len(q)]) + plain[idx+len(q):]
	}
	return out
}

func (m *Model) queueLines() []string {
	var out []string
	for _, s := range m.queue.Steering {
		out = append(out, m.st.warning.Render("↳ steer: ")+m.st.muted.Render(ansi.Truncate(oneLine(s), m.width-12, "…")))
	}
	for _, s := range m.queue.FollowUps {
		out = append(out, m.st.accent.Render("↳ queued: ")+m.st.muted.Render(ansi.Truncate(oneLine(s), m.width-12, "…")))
	}
	return out
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func (m *Model) inputView() []string {
	w := max(10, m.width)
	borderColor := m.theme.Border
	if m.busy {
		borderColor = m.theme.Accent
	}
	bs := lipgloss.NewStyle().Foreground(c(borderColor))
	label := ""
	switch {
	case m.dlg != nil:
		return m.dialogView()
	case m.search != nil:
		label = " search "
	case m.busy:
		label = " enter: steer · alt+enter: queue · esc: cancel "
	case m.agent.Session().BuildContext().Aside:
		label = " side conversation (read-only) · /back to return "
	}
	top := "─"
	if label != "" {
		top += m.st.muted.Render(label)
	}
	top = bs.Render("╭") + bs.Render(top)
	top += bs.Render(strings.Repeat("─", max(0, w-ansi.StringWidth(stripANSI(top))-1)) + "╮")
	var mid []string
	if m.search != nil {
		info := ""
		if m.search.query != "" {
			if n := len(m.search.matches); n > 0 {
				info = m.st.muted.Render(fmt.Sprintf("  %d/%d · enter next · up prev · esc close", m.search.idx+1, n))
			} else {
				info = m.st.errorS.Render("  no matches")
			}
		}
		mid = []string{" " + m.st.accent.Render("/") + m.search.query + info}
	} else {
		mid = strings.Split(m.input.View(), "\n")
		for i := range mid {
			mid[i] = " " + mid[i]
		}
	}
	bottom := bs.Render("╰" + strings.Repeat("─", max(0, w-2)) + "╯")
	return append(append([]string{top}, mid...), bottom)
}

// statusItems renders status entries sorted by key, so they don't swap
// places between redraws.
func statusItems(statuses map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(statuses)) {
		if v := statuses[k]; v != "" {
			out = append(out, k+": "+v)
		}
	}
	return out
}

func (m *Model) statusLine() string {
	w := max(10, m.width)
	var left []string
	model := m.agent.Model()
	left = append(left, m.st.statusKey.Render(" "+model.ID))
	if m.usage.ContextWindow > 0 {
		pct := float64(m.usage.ContextTokens) * 100 / float64(m.usage.ContextWindow)
		ctx := fmt.Sprintf("ctx %.0f%% (%s/%s)", pct, humanTokens(m.usage.ContextTokens), humanTokens(m.usage.ContextWindow))
		st := m.st.status
		if pct > 80 {
			st = st.Foreground(c(m.theme.Warning))
		}
		left = append(left, st.Render(ctx))
	}
	if cost := m.agent.Cost(); cost > 0 {
		left = append(left, m.st.status.Render(fmt.Sprintf("$%.3f", cost)))
	}
	dir := shortenHome(m.app.Paths.Cwd)
	if m.gitBranch != "" {
		dir += " (" + m.gitBranch + ")"
	}
	left = append(left, m.st.status.Render(dir))
	if m.agent.Session().BuildContext().Aside {
		left = append(left, m.st.status.Foreground(c(m.theme.Warning)).Render("aside"))
	}
	var right []string
	for _, item := range statusItems(m.statuses) {
		right = append(right, m.st.status.Render(item))
	}
	if m.busy {
		right = append(right, m.st.statusKey.Render(spinnerFrames[m.frame%len(spinnerFrames)]+" working"))
	}
	if m.flash != "" && time.Now().Before(m.flashUntil) {
		right = append(right, m.st.status.Foreground(c(m.theme.Warning)).Render(m.flash))
	}
	sep := m.st.status.Render(" · ")
	l := strings.Join(left, sep)
	r := strings.Join(right, sep) + m.st.status.Render(" ")
	gap := w - ansi.StringWidth(l) - ansi.StringWidth(r)
	if gap < 1 {
		l = ansi.Truncate(l, max(0, w-ansi.StringWidth(r)-1), "…")
		gap = max(0, w-ansi.StringWidth(l)-ansi.StringWidth(r))
	}
	return l + m.st.status.Render(strings.Repeat(" ", gap)) + r
}

func shortenHome(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h) {
		return "~" + p[len(h):]
	}
	return p
}
