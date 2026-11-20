package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/session"
)

// builtinCommands lists the built-in slash commands.
var builtinCommands = []commandInfo{
	{"model", "switch model"},
	{"new", "start a new session"},
	{"resume", "resume a previous session"},
	{"rewind", "go back to an earlier message (conversation only)"},
	{"fork", "copy the conversation up to a message into a new session"},
	{"tree", "browse all branches of this session"},
	{"aside", "start a read-only side conversation"},
	{"back", "return from a side conversation [note | --summarize]"},
	{"compact", "summarize older messages to free context"},
	{"context", "show what is sent to the model"},
	{"label", "label the current position"},
	{"copy", "copy the last answer to the clipboard [all]"},
	{"theme", "change the color theme"},
	{"config", "change and save settings [key value]"},
	{"extensions", "list extensions and turn them on or off"},
	{"reload", "restart extensions and reload context files"},
	{"keybindings", "list the key bindings"},
	{"help", "show keys and commands"},
	{"quit", "exit Rho"},
}

// ExtensionCommands is set by the extension layer to add slash commands.
type ExtensionCommands interface {
	Commands() []ExtensionCommand
	Run(ctx context.Context, name, args string) error
	Describe() string
	Reload(ctx context.Context) error
	List() []ExtensionInfo
	// SetEnabled starts or stops one extension and saves the choice. The
	// note, if any, warns that the choice won't hold on the next start.
	SetEnabled(ctx context.Context, id string, on bool) (note string, err error)
}

// ExtensionInfo describes a discovered extension.
type ExtensionInfo struct {
	// ID is the name the extension was found under.
	ID       string
	Name     string
	Kind     string
	Project  bool
	Status   string
	Err      string
	Tools    int
	Commands int
	Hooks    int
	Disabled bool
}

// ExtensionCommand is a slash command provided by an extension.
type ExtensionCommand struct {
	Name        string
	Description string
}

func (m *Model) commandList() []commandInfo {
	cmds := append([]commandInfo(nil), builtinCommands...)
	for _, t := range m.app.Templates {
		cmds = append(cmds, commandInfo{t.Name, t.Description})
	}
	if m.ext != nil {
		for _, c := range m.ext.Commands() {
			cmds = append(cmds, commandInfo{c.Name, c.Description})
		}
	}
	sort.SliceStable(cmds, func(i, j int) bool { return cmds[i].name < cmds[j].name })
	return cmds
}

func (m *Model) notice(level, text string) {
	m.addItem(&noticeItem{level: level, text: text})
}

// noticeAfterReload shows a notice once the pending session change has
// reloaded the transcript.
func (m *Model) noticeAfterReload(level, text string) {
	m.postReload = append(m.postReload, &noticeItem{level: level, text: text})
}

func (m *Model) async(f func() error, done func(m *Model) tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		return asyncResultMsg{err: f(), done: done}
	}
}

func (m *Model) runCommand(line string) tea.Cmd {
	name, args, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	args = strings.TrimSpace(args)
	idle := func() bool {
		if m.busy {
			m.notice("warn", "/"+name+" is not available while the agent is working (esc to cancel)")
			return false
		}
		return true
	}
	switch name {
	case "quit", "exit", "q":
		return m.quit()
	case "help":
		m.addItem(&textItem{title: "Rho help", body: m.helpText(), md: true})
	case "keybindings", "keys":
		m.addItem(&textItem{title: "Key bindings", body: keyBindingsText(), md: true})
	case "model":
		if !idle() {
			return nil
		}
		if args != "" {
			if err := m.agent.SetModel(args); err != nil {
				m.notice("error", err.Error())
			}
			return nil
		}
		m.modelPicker()
	case "new":
		if !idle() {
			return nil
		}
		m.noticeAfterReload("info", "new session")
		if err := m.agent.SetSession(m.app.NewSession()); err != nil {
			m.postReload = nil
			m.notice("error", err.Error())
		}
	case "resume":
		if !idle() {
			return nil
		}
		m.resumePicker()
	case "rewind":
		if !idle() {
			return nil
		}
		m.rewindPicker()
	case "fork":
		if !idle() {
			return nil
		}
		m.forkPicker()
	case "tree":
		if !idle() {
			return nil
		}
		m.treePicker()
	case "aside":
		if !idle() {
			return nil
		}
		if err := m.agent.StartAside(); err != nil {
			m.notice("error", err.Error())
		}
	case "back":
		if !idle() {
			return nil
		}
		if args == "--summarize" || args == "-s" {
			m.notice("info", "summarizing the side conversation…")
			var note string
			return m.async(func() error {
				var err error
				note, err = m.agent.SummarizeAside(context.Background())
				return err
			}, func(m *Model) tea.Cmd {
				if note != "" {
					m.input.SetValue("/back " + note)
					m.afterInputChange()
					m.setFlash("edit the note, then press enter")
				}
				return nil
			})
		}
		if err := m.agent.EndAside(args); err != nil {
			m.notice("error", err.Error())
		}
	case "compact":
		if !idle() {
			return nil
		}
		m.busy = true
		return m.async(func() error { return m.agent.Compact(context.Background()) }, func(m *Model) tea.Cmd {
			m.busy = false
			return nil
		})
	case "context":
		m.contextReport()
	case "label":
		leaf := m.agent.Session().Leaf()
		if leaf == "" {
			m.notice("warn", "nothing to label yet")
			return nil
		}
		if err := m.agent.Label(leaf, args); err != nil {
			m.notice("error", err.Error())
		} else if args == "" {
			m.notice("info", "label removed")
		} else {
			m.notice("info", "labeled: "+args)
		}
	case "copy":
		return m.copyCmd(args == "all")
	case "theme":
		if args != "" {
			return m.applyThemeName(args)
		}
		var items []pickerItem
		for _, n := range ThemeNames(m.themes) {
			detail := ""
			if n == m.theme.Name {
				detail = "current"
			}
			items = append(items, pickerItem{title: n, detail: detail, value: n})
		}
		m.picker = newPicker("Theme", items, func(it pickerItem) tea.Cmd { return m.applyThemeName(it.value.(string)) })
	case "config":
		if !idle() {
			return nil
		}
		return m.configCommand(args)
	case "extensions":
		if m.ext == nil {
			m.notice("info", "extensions are turned off for this run")
			return nil
		}
		if !idle() {
			return nil
		}
		m.extensionsPicker()
	case "reload":
		if !idle() {
			return nil
		}
		return m.reload()
	default:
		for _, t := range m.app.Templates {
			if t.Name == name {
				return m.sendText(t.Expand(args), false)
			}
		}
		if m.ext != nil {
			for _, c := range m.ext.Commands() {
				if c.Name == name {
					return m.async(func() error { return m.ext.Run(context.Background(), name, args) }, nil)
				}
			}
		}
		m.notice("error", "unknown command /"+name+" (see /help)")
	}
	return nil
}

func (m *Model) applyThemeName(name string) tea.Cmd {
	t, ok := m.themes[name]
	if !ok {
		m.notice("error", "unknown theme "+name)
		return nil
	}
	m.setTheme(t)
	m.applyInputStyles()
	m.notice("info", fmt.Sprintf("theme: %s (/config theme %s keeps it)", name, name))
	return nil
}

func (m *Model) copyCmd(all bool) tea.Cmd {
	text := m.app.CopyText(all)
	if text == "" {
		m.notice("warn", "nothing to copy")
		return nil
	}
	m.setFlash("copied to clipboard")
	return tea.SetClipboard(text)
}

func (m *Model) modelPicker() {
	cur := m.agent.Model().ID
	var items []pickerItem
	for _, md := range m.app.Config.Models {
		detail := md.Provider
		if md.ContextWindow > 0 {
			detail += fmt.Sprintf(" · %s ctx", humanTokens(md.ContextWindow))
		}
		if md.ID == cur {
			detail += " · current"
		}
		items = append(items, pickerItem{title: md.ID, detail: detail, value: md.ID})
	}
	if len(items) == 0 {
		m.notice("warn", "no models configured; use /model provider/name")
		return
	}
	m.picker = newPicker("Model", items, func(it pickerItem) tea.Cmd {
		if err := m.agent.SetModel(it.value.(string)); err != nil {
			m.notice("error", err.Error())
		}
		return nil
	})
}

func (m *Model) resumePicker() {
	list, err := session.List(m.app.SessionDir())
	if err != nil {
		m.notice("error", err.Error())
		return
	}
	cur := m.agent.Session().Path()
	var items []pickerItem
	for _, s := range list {
		if s.Path == cur {
			continue
		}
		detail := fmt.Sprintf("%s · %d msgs · %s", s.Modified.Format("Jan 2 15:04"), s.Messages, humanBytes(s.Size))
		if s.Model != "" {
			detail += " · " + s.Model
		}
		items = append(items, pickerItem{title: s.Title, detail: detail, value: s.Path})
	}
	if len(items) == 0 {
		m.notice("info", "no other sessions in this directory")
		return
	}
	m.picker = newPicker("Resume session", items, func(it pickerItem) tea.Cmd {
		s, err := session.Open(it.value.(string))
		if err != nil {
			m.notice("error", err.Error())
			return nil
		}
		if err := m.agent.SetSession(s); err != nil {
			m.notice("error", err.Error())
		}
		return nil
	})
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// pathMessages lists message entries on the current path, newest first.
func (m *Model) pathMessages(roles ...provider.Role) []pickerItem {
	var items []pickerItem
	for _, r := range m.app.PathMessages(roles...) {
		title := oneLine(r.Text)
		if title == "" {
			if r.ToolName != "" {
				title = "[tool calls: " + r.ToolName + "…]"
			} else {
				title = "(empty)"
			}
		}
		detail := string(r.Role) + " · " + r.TS.Local().Format("15:04")
		if r.Label != "" {
			detail = "🏷 " + r.Label + " · " + detail
		}
		items = append(items, pickerItem{title: title, detail: detail, value: r.ID})
	}
	return items
}

func (m *Model) rewindPicker() {
	items := m.pathMessages(provider.RoleUser)
	if len(items) == 0 {
		m.notice("info", "nothing to rewind")
		return
	}
	m.picker = newPicker("Rewind to before…", items, func(it pickerItem) tea.Cmd {
		text, err := m.agent.RewindTo(it.value.(string))
		if err != nil {
			m.notice("error", err.Error())
			return nil
		}
		m.noticeAfterReload("banner", "Rewound the conversation. Files on disk were NOT reverted.")
		m.input.SetValue(text)
		m.afterInputChange()
		return nil
	})
	m.picker.hint = "the selected message goes back into the editor · esc close"
}

func (m *Model) forkPicker() {
	items := m.pathMessages(provider.RoleUser, provider.RoleAssistant)
	// The leaf may be a non-message entry (a side-conversation note or a
	// compaction) that the message list leaves out.
	if leaf := m.agent.Session().Leaf(); leaf != "" && (len(items) == 0 || items[0].value != leaf) {
		items = append([]pickerItem{{title: "Current position (everything so far)", detail: "latest", value: leaf}}, items...)
	}
	if len(items) == 0 {
		m.notice("info", "nothing to fork")
		return
	}
	m.picker = newPicker("Fork from (inclusive)…", items, func(it pickerItem) tea.Cmd {
		s, err := m.app.Fork(it.value.(string))
		if err != nil {
			m.notice("error", err.Error())
			return nil
		}
		m.noticeAfterReload("info", "forked into a new session: "+shortenHome(s.Path()))
		if err := m.agent.SetSession(s); err != nil {
			m.postReload = nil
			m.notice("error", err.Error())
		}
		return nil
	})
}

func (m *Model) treePicker() {
	labels := map[string]string{}
	var items []pickerItem
	cursor := 0
	for _, n := range m.app.Tree() {
		detail := n.TS.Local().Format("Jan 2 15:04")
		if n.Current {
			detail = "● current · " + detail
			cursor = len(items)
		} else if n.OnPath {
			detail = "│ " + detail
		}
		if n.Label != "" {
			labels[n.ID] = n.Label
			detail = "🏷 " + n.Label + " · " + detail
		}
		items = append(items, pickerItem{title: treeTitle(n), detail: detail, value: n.ID, depth: min(n.Depth, 12)})
	}
	if len(items) == 0 {
		m.notice("info", "the session is empty")
		return
	}
	p := newPicker("Session tree", items, func(it pickerItem) tea.Cmd {
		m.noticeAfterReload("banner", "Moved to another point in the conversation. Files on disk were NOT changed.")
		if err := m.agent.Checkout(it.value.(string)); err != nil {
			m.postReload = nil
			m.notice("error", err.Error())
		}
		return nil
	})
	p.hint = "enter check out · ctrl+l label · esc close"
	p.keys = map[string]func(pickerItem) tea.Cmd{
		"ctrl+l": func(it pickerItem) tea.Cmd {
			id := it.value.(string)
			m.picker = nil
			m.openInput("Label", "label for this entry (empty to clear)", labels[id], func(v string) tea.Cmd {
				if err := m.agent.Label(id, v); err != nil {
					m.notice("error", err.Error())
				}
				return nil
			})
			return nil
		},
	}
	p.cursor = cursor
	m.picker = p
}

func treeTitle(n app.TreeNode) string {
	switch n.Kind {
	case string(provider.RoleUser):
		return "› " + oneLine(n.Text)
	case string(provider.RoleAssistant):
		return "  " + oneLine(n.Text)
	case app.NodeCompaction:
		return "⇣ compaction"
	case app.NodeAsideStart:
		return "↪ side conversation"
	case app.NodeAsideNote:
		return "↩ note: " + oneLine(n.Text)
	case app.NodeModelChange:
		return "⚙ model: " + n.Text
	}
	return n.Text
}

func (m *Model) contextReport() {
	r, err := m.agent.ContextReport()
	if err != nil {
		m.notice("error", err.Error())
		return
	}
	var b strings.Builder
	b.WriteString("| Part | Source | Tokens |\n|---|---|---|\n")
	for _, p := range r.Parts {
		src := p.Path
		if src == "" {
			src = "built-in"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", p.Name, shortenHome(src), humanTokens(p.Tokens))
	}
	fmt.Fprintf(&b, "| Tool definitions | %d tools | %s |\n", len(m.agent.Tools()), humanTokens(r.ToolTokens))
	msgs := fmt.Sprintf("%d messages", r.Messages)
	if r.Compacted {
		msgs += " (after compaction)"
	}
	fmt.Fprintf(&b, "| Conversation | %s | %s |\n", msgs, humanTokens(r.MessageTokens))
	fmt.Fprintf(&b, "| **Total (estimate)** | | **%s** |\n", humanTokens(r.Total))
	if r.Window > 0 {
		fmt.Fprintf(&b, "\nContext window: %s tokens (%.0f%% used). Estimates use about 4 characters per token.\n",
			humanTokens(r.Window), float64(r.Total)*100/float64(r.Window))
	}
	if len(m.app.Skills) > 0 {
		b.WriteString("\nSkills: ")
		var names []string
		for _, s := range m.app.Skills {
			names = append(names, s.Name)
		}
		b.WriteString(strings.Join(names, ", "))
		b.WriteString("\n")
	}
	m.addItem(&textItem{title: "Context", body: b.String(), md: true})
}

func (m *Model) helpText() string {
	var b strings.Builder
	b.WriteString(`**Keys**

- enter: send · shift+enter, alt+enter, ctrl+j: new line
- while working: enter steers (interrupts after the current tool), alt+enter queues a follow-up, esc cancels
- ctrl+c: cancel / clear input / press twice to quit · ctrl+d on empty input: quit
- ctrl+o: expand tool output and diffs · ctrl+t: show thinking
- ctrl+f: search the transcript · pgup/pgdown, shift+↑/↓, mouse wheel: scroll · ctrl+end: follow
- ↑/↓ on the first/last line: prompt history · tab: accept completion
- @path attaches a file · start a message with // to send a literal slash
- Drag with the mouse to select text and copy it; shift+drag uses the terminal's own selection (or use /copy)

**Commands**

`)
	for _, c := range m.commandList() {
		fmt.Fprintf(&b, "- /%s: %s\n", c.name, c.desc)
	}
	return b.String()
}
