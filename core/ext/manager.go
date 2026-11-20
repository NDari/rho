package ext

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/NDari/rho/core/compact"
	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/ext/rpc"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/tools"
)

// ManifestName is the manifest file for executable extensions.
const ManifestName = "rho-extension.toml"

// Extension kinds.
const (
	KindGo   = "go"
	KindExec = "exec"
)

// Extension states.
const (
	StatusRunning     = "running"
	StatusFailed      = "failed"
	StatusDisabled    = "disabled"
	StatusNotApproved = "not approved"
	StatusStopped     = "stopped"
)

// Candidate is a discovered extension.
type Candidate struct {
	Name    string
	Path    string
	Kind    string
	Project bool
	Command string
	Args    []string
	Dir     string
}

type manifest struct {
	Name    string   `toml:"name"`
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
}

// Extension is a running (or failed) extension process.
type Extension struct {
	Candidate
	// ID is the name the extension was found under, which
	// extensions.disabled refers to. Name may be replaced by the name the
	// extension reports.
	ID     string
	mu     sync.Mutex
	Status string
	Err    string
	Info   InitializeResult
	conn   *rpc.Conn
	kill   func()
	logs   []string
}

// Lock and Unlock guard Status, Err and Info, which change as the
// extension starts and stops.
func (e *Extension) Lock() { e.mu.Lock() }

// Unlock releases Lock.
func (e *Extension) Unlock() { e.mu.Unlock() }

func (e *Extension) log(line string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.logs = append(e.logs, line)
	if len(e.logs) > 200 {
		e.logs = e.logs[len(e.logs)-200:]
	}
}

func (e *Extension) lastLogs(n int) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.logs) <= n {
		return append([]string(nil), e.logs...)
	}
	return append([]string(nil), e.logs[len(e.logs)-n:]...)
}

func (e *Extension) hasHook(name string) bool {
	return e.Status == StatusRunning && slices.Contains(e.Info.Hooks, name)
}

// AgentControl is the subset of the agent extensions may drive.
type AgentControl interface {
	Busy() bool
	Prompt(provider.Message) error
	FollowUp(string) error
}

// Launcher starts an extension process and returns its stdio.
type Launcher func(c Candidate) (stdout io.ReadCloser, stdin io.WriteCloser, stderr io.ReadCloser, kill func(), err error)

// Options configure a Manager.
type Options struct {
	Paths   config.Paths
	Config  config.Config
	Version string
	UI      UI
	Agent   AgentControl
	// Builtin are tool names extensions may not override.
	Builtin []string
	// ExtHost is the program that runs Go source extensions, as
	// "ExtHost ext-host <path>". Empty means the running executable.
	ExtHost string
	// Launch overrides process creation (for tests).
	Launch Launcher
	// Sink receives notices.
	Sink events.Sink
}

// Manager discovers, starts and talks to extensions.
type Manager struct {
	o        Options
	trust    *TrustStore
	mu       sync.Mutex
	exts     []*Extension
	progress sync.Map // call id -> func(string)
	warnings []string
}

// NewManager creates a manager.
func NewManager(o Options) *Manager {
	if o.Sink == nil {
		o.Sink = func(events.Event) {}
	}
	return &Manager{o: o, trust: LoadTrust(o.Paths.Home)}
}

// SetUI sets the frontend used for UI requests.
func (m *Manager) SetUI(ui UI) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.o.UI = ui
}

// SetAgent sets the agent controlled by session/append_message.
func (m *Manager) SetAgent(a AgentControl) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.o.Agent = a
}

func (m *Manager) ui() UI {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.o.UI
}

func (m *Manager) timeout() time.Duration {
	if t := m.o.Config.Extensions.Timeout; t > 0 {
		return time.Duration(t) * time.Second
	}
	return 30 * time.Second
}

// Discover lists extensions in ~/.rho/extensions and .rho/extensions.
func (m *Manager) Discover() ([]Candidate, []string) {
	var out []Candidate
	var warnings []string
	dirs := []struct {
		path    string
		project bool
	}{
		{filepath.Join(m.o.Paths.Home, "extensions"), false},
		{filepath.Join(m.o.Paths.ProjectDir, "extensions"), true},
	}
	seen := map[string]bool{}
	for _, d := range dirs {
		if seen[d.path] {
			continue
		}
		seen[d.path] = true
		entries, err := os.ReadDir(d.path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") {
				continue
			}
			p := filepath.Join(d.path, e.Name())
			c, ok, err := candidateAt(p, e.IsDir())
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", p, err))
				continue
			}
			if ok {
				c.Project = d.project
				out = append(out, c)
			}
		}
	}
	return out, warnings
}

func candidateAt(p string, isDir bool) (Candidate, bool, error) {
	if !isDir {
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			return Candidate{Name: strings.TrimSuffix(filepath.Base(p), ".go"), Path: p, Kind: KindGo, Dir: filepath.Dir(p)}, true, nil
		}
		return Candidate{}, false, nil
	}
	mpath := filepath.Join(p, ManifestName)
	if data, err := os.ReadFile(mpath); err == nil {
		var mf manifest
		if _, err := toml.Decode(string(data), &mf); err != nil {
			return Candidate{}, false, fmt.Errorf("invalid %s: %w", ManifestName, err)
		}
		if mf.Command == "" {
			return Candidate{}, false, fmt.Errorf("%s: command is required", ManifestName)
		}
		name := mf.Name
		if name == "" {
			name = filepath.Base(p)
		}
		expand := func(s string) string { return strings.ReplaceAll(s, "${dir}", p) }
		cmd := expand(mf.Command)
		if strings.ContainsAny(cmd, `/\`) && !filepath.IsAbs(cmd) {
			cmd = filepath.Join(p, cmd)
		}
		args := make([]string, len(mf.Args))
		for i, a := range mf.Args {
			args[i] = expand(a)
		}
		return Candidate{Name: name, Path: p, Kind: KindExec, Command: cmd, Args: args, Dir: p}, true, nil
	}
	if gofiles, _ := filepath.Glob(filepath.Join(p, "*.go")); len(gofiles) > 0 {
		return Candidate{Name: filepath.Base(p), Path: p, Kind: KindGo, Dir: p}, true, nil
	}
	return Candidate{}, false, nil
}

// Start discovers and starts all extensions. Problems are recorded per
// extension rather than returned.
func (m *Manager) Start(ctx context.Context) {
	cands, warnings := m.Discover()
	m.mu.Lock()
	m.warnings = warnings
	m.mu.Unlock()
	for _, w := range warnings {
		m.o.Sink(events.Notice{Level: "warn", Text: "extension: " + w})
	}
	m.mu.Lock()
	disabled := m.o.Config.Extensions.Disabled
	m.mu.Unlock()
	var wg sync.WaitGroup
	var started []*Extension
	for _, c := range cands {
		e := &Extension{Candidate: c, ID: c.Name}
		started = append(started, e)
		if slices.Contains(disabled, c.Name) {
			e.Status = StatusDisabled
			continue
		}
		if c.Project && !m.approve(ctx, c) {
			e.Status = StatusNotApproved
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.startOne(ctx, e)
		}()
	}
	wg.Wait()
	m.mu.Lock()
	m.exts = started
	m.mu.Unlock()
	m.resolveConflicts()
	for _, e := range started {
		if e.Status == StatusFailed {
			m.o.Sink(events.Notice{Level: "error", Text: fmt.Sprintf("extension %s failed: %s", e.Name, e.Err)})
		}
	}
	m.callHooks(ctx, HookSessionStart, HookParams{})
}

func (m *Manager) approve(ctx context.Context, c Candidate) bool {
	hash, err := HashPath(c.Path)
	if err != nil {
		return false
	}
	if m.trust.Trusted(c.Path, hash) {
		return true
	}
	ui := m.ui()
	if ui == nil {
		return false
	}
	what := "a Go extension"
	if c.Kind == KindExec {
		what = fmt.Sprintf("the command %q", strings.TrimSpace(c.Command+" "+strings.Join(c.Args, " ")))
	}
	ok, err := ui.Confirm(ctx, "Run project extension "+c.Name+"?",
		fmt.Sprintf("%s runs %s with your permissions. Approve only if you trust this repository. (You will be asked again if it changes.)", c.Path, what))
	if err != nil || !ok {
		return false
	}
	if err := m.trust.Approve(c.Path, hash); err != nil {
		m.o.Sink(events.Notice{Level: "warn", Text: "could not save approval: " + err.Error()})
	}
	return true
}

func (m *Manager) startOne(ctx context.Context, e *Extension) {
	fail := func(err error) {
		e.mu.Lock()
		e.Status = StatusFailed
		e.Err = err.Error()
		e.mu.Unlock()
		if e.kill != nil {
			e.kill()
		}
	}
	stdout, stdin, stderr, kill, err := m.launch(e.Candidate)
	if err != nil {
		fail(err)
		return
	}
	e.kill = func() {
		_ = stdin.Close()
		kill()
	}
	if stderr != nil {
		go func() {
			sc := bufio.NewScanner(stderr)
			for sc.Scan() {
				e.log(sc.Text())
			}
		}()
	}
	e.conn = rpc.NewConn(stdout, stdin, m.handler(e))
	ictx, cancel := context.WithTimeout(ctx, m.timeout())
	defer cancel()
	var res InitializeResult
	err = e.conn.Call(ictx, "initialize", InitializeParams{ProtocolVersion: ProtocolVersion, RhoVersion: m.o.Version, Cwd: m.o.Paths.Cwd}, &res)
	if err != nil {
		if logs := e.lastLogs(5); len(logs) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.Join(logs, " | "))
		}
		fail(fmt.Errorf("initialize: %w", err))
		return
	}
	if res.ProtocolVersion != ProtocolVersion {
		fail(fmt.Errorf("protocol version %d is not supported (Rho speaks version %d)", res.ProtocolVersion, ProtocolVersion))
		return
	}
	if res.Name != "" {
		e.Name = res.Name
	}
	e.mu.Lock()
	e.Info = res
	e.Status = StatusRunning
	e.mu.Unlock()
	go func() {
		<-e.conn.Done()
		e.mu.Lock()
		wasRunning := e.Status == StatusRunning
		if wasRunning {
			e.Status = StatusFailed
			e.Err = "exited unexpectedly"
			if logs := e.lastLogs(3); len(logs) > 0 {
				e.Err += ": " + strings.Join(logs, " | ")
			}
		}
		msg := e.Err
		e.mu.Unlock()
		if wasRunning {
			m.o.Sink(events.Notice{Level: "error", Text: fmt.Sprintf("extension %s crashed (%s); /reload to restart", e.Name, msg)})
		}
	}()
}

// resolveConflicts drops tools and commands whose names are taken.
func (m *Manager) resolveConflicts() {
	taken := map[string]string{}
	for _, b := range m.o.Builtin {
		taken[b] = "built-in"
	}
	cmdTaken := map[string]string{}
	for _, e := range m.running() {
		var keep []ToolInfo
		for _, t := range e.Info.Tools {
			if owner, ok := taken[t.Name]; ok {
				m.o.Sink(events.Notice{Level: "warn", Text: fmt.Sprintf("extension %s: tool %q already provided by %s; ignored", e.Name, t.Name, owner)})
				continue
			}
			taken[t.Name] = e.Name
			keep = append(keep, t)
		}
		var keepC []CommandInfo
		for _, c := range e.Info.Commands {
			if owner, ok := cmdTaken[c.Name]; ok {
				m.o.Sink(events.Notice{Level: "warn", Text: fmt.Sprintf("extension %s: command /%s already provided by %s; ignored", e.Name, c.Name, owner)})
				continue
			}
			cmdTaken[c.Name] = e.Name
			keepC = append(keepC, c)
		}
		e.mu.Lock()
		e.Info.Tools = keep
		e.Info.Commands = keepC
		e.mu.Unlock()
	}
}

func (m *Manager) running() []*Extension {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Extension
	for _, e := range m.exts {
		e.mu.Lock()
		ok := e.Status == StatusRunning
		e.mu.Unlock()
		if ok {
			out = append(out, e)
		}
	}
	return out
}

// Extensions returns all known extensions.
func (m *Manager) Extensions() []*Extension {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*Extension(nil), m.exts...)
}

// Stop shuts down all extensions.
func (m *Manager) Stop() {
	m.callHooks(context.Background(), HookSessionEnd, HookParams{})
	m.mu.Lock()
	exts := m.exts
	m.exts = nil
	m.mu.Unlock()
	for _, e := range exts {
		stopOne(e, StatusStopped)
	}
}

// stopOne shuts e down and sets its status.
func stopOne(e *Extension, status string) {
	e.mu.Lock()
	running := e.Status == StatusRunning
	e.Status = status
	e.mu.Unlock()
	if running && e.conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = e.conn.Call(ctx, "shutdown", struct{}{}, nil)
		cancel()
	}
	if e.kill != nil {
		e.kill()
	}
}

// SetEnabled starts or stops one extension (by ID), leaving the others
// running, and records the choice in the manager's config (not on disk).
// Enabling a project extension asks for approval if needed.
func (m *Manager) SetEnabled(ctx context.Context, name string, on bool) error {
	m.mu.Lock()
	idx := slices.IndexFunc(m.exts, func(e *Extension) bool { return e.ID == name })
	if idx == -1 {
		m.mu.Unlock()
		return fmt.Errorf("no extension named %q", name)
	}
	e := m.exts[idx]
	disabled := slices.DeleteFunc(slices.Clone(m.o.Config.Extensions.Disabled), func(n string) bool { return n == name })
	if !on {
		disabled = append(disabled, name)
	}
	m.o.Config.Extensions.Disabled = disabled
	m.mu.Unlock()

	e.mu.Lock()
	status := e.Status
	e.mu.Unlock()
	if !on {
		if status == StatusRunning && e.hasHook(HookSessionEnd) {
			hctx, cancel := context.WithTimeout(ctx, m.timeout())
			_ = e.conn.Call(hctx, "hook/"+HookSessionEnd, HookParams{}, nil)
			cancel()
		}
		stopOne(e, StatusDisabled)
		return nil
	}
	if status == StatusRunning {
		return nil
	}
	// Start a fresh copy, so nothing from an earlier run lingers.
	c := e.Candidate
	c.Name = e.ID
	fresh := &Extension{Candidate: c, ID: e.ID}
	if fresh.Project && !m.approve(ctx, fresh.Candidate) {
		fresh.Status = StatusNotApproved
	} else {
		m.startOne(ctx, fresh)
	}
	m.mu.Lock()
	if i := slices.Index(m.exts, e); i != -1 {
		m.exts[i] = fresh
	}
	m.mu.Unlock()
	m.resolveConflicts()
	fresh.mu.Lock()
	status, errText := fresh.Status, fresh.Err
	fresh.mu.Unlock()
	switch status {
	case StatusRunning:
		if fresh.hasHook(HookSessionStart) {
			hctx, cancel := context.WithTimeout(ctx, m.timeout())
			_ = fresh.conn.Call(hctx, "hook/"+HookSessionStart, HookParams{}, nil)
			cancel()
		}
		return nil
	case StatusNotApproved:
		return fmt.Errorf("extension %s was not approved", name)
	default:
		return fmt.Errorf("extension %s failed: %s", name, errText)
	}
}

// Reload restarts all extensions.
func (m *Manager) Reload(ctx context.Context) error {
	m.Stop()
	m.Start(ctx)
	return nil
}

// handler serves requests from an extension.
func (m *Manager) handler(e *Extension) rpc.Handler {
	return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		ui := m.ui()
		switch method {
		case "tool/progress":
			var p ToolProgressParams
			if json.Unmarshal(params, &p) == nil {
				if f, ok := m.progress.Load(p.CallID); ok {
					f.(func(string))(p.Text)
				}
			}
			return nil, nil
		case "log":
			var p LogParams
			_ = json.Unmarshal(params, &p)
			e.log(fmt.Sprintf("[%s] %s", p.Level, p.Message))
			return struct{}{}, nil
		case "ui/notify":
			var p NotifyParams
			_ = json.Unmarshal(params, &p)
			if ui != nil {
				ui.Notify(p.Level, p.Text)
			} else {
				m.o.Sink(events.Notice{Level: orDefault(p.Level, "info"), Text: e.Name + ": " + p.Text})
			}
			return struct{}{}, nil
		case "ui/status":
			var p StatusParams
			_ = json.Unmarshal(params, &p)
			if ui != nil {
				ui.SetStatus(orDefault(p.Key, e.Name), p.Text)
			}
			return struct{}{}, nil
		case "ui/confirm":
			var p ConfirmParams
			_ = json.Unmarshal(params, &p)
			if ui == nil {
				return ConfirmResult{}, nil
			}
			// A dismissed or failed prompt counts as "no".
			ok, err := ui.Confirm(ctx, p.Title, p.Message)
			return ConfirmResult{Confirmed: ok && err == nil}, nil
		case "ui/select":
			var p SelectParams
			_ = json.Unmarshal(params, &p)
			if ui == nil {
				return ValueResult{Cancelled: true}, nil
			}
			v, err := ui.Select(ctx, p.Title, p.Options)
			return ValueResult{Value: v, Cancelled: err != nil}, nil
		case "ui/input":
			var p InputParams
			_ = json.Unmarshal(params, &p)
			if ui == nil {
				return ValueResult{Cancelled: true}, nil
			}
			v, err := ui.Input(ctx, p.Title, p.Placeholder)
			return ValueResult{Value: v, Cancelled: err != nil}, nil
		case "session/append_message":
			var p AppendMessageParams
			if err := json.Unmarshal(params, &p); err != nil || p.Text == "" {
				return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "text is required"}
			}
			m.mu.Lock()
			ag := m.o.Agent
			m.mu.Unlock()
			if ag == nil {
				return nil, errors.New("no agent")
			}
			if ag.Busy() {
				return struct{}{}, ag.FollowUp(p.Text)
			}
			if p.Trigger {
				return struct{}{}, ag.Prompt(provider.UserText(p.Text))
			}
			return struct{}{}, ag.FollowUp(p.Text)
		}
		return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "unknown method " + method}
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Tools returns adapters for all extension tools.
func (m *Manager) Tools() []tools.Tool {
	var out []tools.Tool
	for _, e := range m.running() {
		for _, t := range e.Info.Tools {
			out = append(out, &extTool{m: m, ext: e, info: t})
		}
	}
	return out
}

// Command is an extension slash command.
type Command struct {
	CommandInfo
	Extension string
}

// Commands returns all extension commands.
func (m *Manager) Commands() []Command {
	var out []Command
	for _, e := range m.running() {
		for _, c := range e.Info.Commands {
			out = append(out, Command{CommandInfo: c, Extension: e.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RunCommand runs an extension command.
func (m *Manager) RunCommand(ctx context.Context, name, args string) error {
	for _, e := range m.running() {
		for _, c := range e.Info.Commands {
			if c.Name == name {
				// Commands may show UI and wait for the user, so they get a
				// generous timeout.
				cctx, cancel := context.WithTimeout(ctx, 10*m.timeout())
				defer cancel()
				return e.conn.Call(cctx, "command/run", CommandRunParams{Name: name, Args: args}, nil)
			}
		}
	}
	return fmt.Errorf("unknown command /%s", name)
}

// Describe returns a human-readable status report.
func (m *Manager) Describe() string {
	exts := m.Extensions()
	if len(exts) == 0 {
		return "No extensions found. Put extensions in ~/.rho/extensions (global) or .rho/extensions (project)."
	}
	var b strings.Builder
	for _, e := range exts {
		e.mu.Lock()
		scope := "global"
		if e.Project {
			scope = "project"
		}
		fmt.Fprintf(&b, "%s (%s, %s) · %s\n  %s\n", e.Name, e.Kind, scope, e.Status, e.Path)
		if e.Err != "" {
			fmt.Fprintf(&b, "  error: %s\n", e.Err)
		}
		for _, t := range e.Info.Tools {
			fmt.Fprintf(&b, "  tool: %s\n", t.Name)
		}
		for _, c := range e.Info.Commands {
			fmt.Fprintf(&b, "  command: /%s\n", c.Name)
		}
		if len(e.Info.Hooks) > 0 {
			fmt.Fprintf(&b, "  hooks: %s\n", strings.Join(e.Info.Hooks, ", "))
		}
		e.mu.Unlock()
		for _, l := range e.lastLogs(5) {
			fmt.Fprintf(&b, "  log: %s\n", l)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Manager) callHooks(ctx context.Context, hook string, p HookParams) {
	for _, e := range m.running() {
		if !e.hasHook(hook) {
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, m.timeout())
		_ = e.conn.Call(hctx, "hook/"+hook, p, nil)
		cancel()
	}
}

// BeforeToolCall implements agent.Hooks.
func (m *Manager) BeforeToolCall(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, string) {
	for _, e := range m.running() {
		if !e.hasHook(HookBeforeToolCall) {
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, 10*m.timeout())
		var res HookResult
		err := e.conn.Call(hctx, "hook/"+HookBeforeToolCall, HookParams{Tool: name, Args: args}, &res)
		cancel()
		if err != nil {
			e.log("before_tool_call: " + err.Error())
			continue
		}
		if res.Block != "" {
			return nil, res.Block + " (blocked by extension " + e.Name + ")"
		}
		if len(res.Args) > 0 {
			args = res.Args
		}
	}
	return args, ""
}

// AfterToolCall implements agent.Hooks.
func (m *Manager) AfterToolCall(ctx context.Context, name string, args json.RawMessage, res tools.Result) tools.Result {
	for _, e := range m.running() {
		if !e.hasHook(HookAfterToolCall) {
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, m.timeout())
		var hr HookResult
		r := toWire(res.Content, res.IsError)
		err := e.conn.Call(hctx, "hook/"+HookAfterToolCall, HookParams{Tool: name, Args: args, Result: &r}, &hr)
		cancel()
		if err != nil {
			e.log("after_tool_call: " + err.Error())
			continue
		}
		if hr.Result != nil {
			res.Content, res.IsError = fromWire(*hr.Result)
		}
	}
	return res
}

// TurnEnd implements agent.Hooks.
func (m *Manager) TurnEnd(ctx context.Context) {
	m.callHooks(ctx, HookTurnEnd, HookParams{})
}

// BeforeCompaction implements agent.Hooks.
func (m *Manager) BeforeCompaction(ctx context.Context, msgs []provider.Message) (string, bool) {
	for _, e := range m.running() {
		if !e.hasHook(HookBeforeCompaction) {
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, 10*m.timeout())
		var hr HookResult
		err := e.conn.Call(hctx, "hook/"+HookBeforeCompaction, HookParams{Transcript: compact.Transcript(msgs, 4000)}, &hr)
		cancel()
		if err == nil && strings.TrimSpace(hr.Summary) != "" {
			return hr.Summary, true
		}
	}
	return "", false
}

func toWire(blocks []provider.Block, isErr bool) ToolResult {
	r := ToolResult{IsError: isErr}
	for _, b := range blocks {
		switch b.Type {
		case provider.BlockText:
			r.Content = append(r.Content, ContentBlock{Type: "text", Text: b.Text})
		case provider.BlockImage:
			r.Content = append(r.Content, ContentBlock{Type: "image", MIME: b.MIME, Data: b.Data})
		}
	}
	return r
}

func fromWire(r ToolResult) ([]provider.Block, bool) {
	var out []provider.Block
	for _, c := range r.Content {
		switch c.Type {
		case "image":
			out = append(out, provider.Block{Type: provider.BlockImage, MIME: c.MIME, Data: c.Data})
		default:
			out = append(out, provider.Text(c.Text))
		}
	}
	if len(out) == 0 {
		out = []provider.Block{provider.Text("(no output)")}
	}
	return out, r.IsError
}

// extTool adapts an extension tool to tools.Tool.
type extTool struct {
	m    *Manager
	ext  *Extension
	info ToolInfo
}

func (t *extTool) Spec() provider.ToolSpec {
	params := t.info.Parameters
	if len(params) == 0 {
		params = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return provider.ToolSpec{Name: t.info.Name, Description: t.info.Description, Parameters: params}
}

func (t *extTool) ReadOnly() bool { return t.info.ReadOnly }

func (t *extTool) Execute(ctx context.Context, _ *tools.Env, args json.RawMessage, progress func(string)) tools.Result {
	t.ext.mu.Lock()
	running := t.ext.Status == StatusRunning
	t.ext.mu.Unlock()
	if !running {
		return tools.Result{Content: []provider.Block{provider.Text("extension " + t.ext.Name + " is not running")}, IsError: true}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	callID := hex.EncodeToString(b[:])
	if progress != nil {
		t.m.progress.Store(callID, progress)
		defer t.m.progress.Delete(callID)
	}
	var res ToolResult
	err := t.ext.conn.Call(ctx, "tool/execute", ToolExecuteParams{CallID: callID, Name: t.info.Name, Args: args}, &res)
	if err != nil {
		if ctx.Err() != nil {
			_ = t.ext.conn.Notify("tool/cancel", map[string]string{"call_id": callID})
			return tools.Result{Content: []provider.Block{provider.Text("Cancelled by the user.")}, IsError: true}
		}
		return tools.Result{Content: []provider.Block{provider.Text("extension error: " + err.Error())}, IsError: true}
	}
	content, isErr := fromWire(res)
	var text strings.Builder
	for _, c := range content {
		text.WriteString(c.Text)
	}
	return tools.Result{Content: content, IsError: isErr, Display: &events.Display{Kind: "text", Text: text.String()}}
}

func (m *Manager) launch(c Candidate) (io.ReadCloser, io.WriteCloser, io.ReadCloser, func(), error) {
	if m.o.Launch != nil {
		return m.o.Launch(c)
	}
	return defaultLauncher(c, m.o.Paths.Cwd, m.o.ExtHost)
}

// defaultLauncher starts the process in the session working directory, so
// relative paths from the model resolve as they do for built-in tools.
func defaultLauncher(c Candidate, cwd, extHost string) (io.ReadCloser, io.WriteCloser, io.ReadCloser, func(), error) {
	var cmd *exec.Cmd
	switch c.Kind {
	case KindGo:
		exe := extHost
		if exe == "" {
			var err error
			if exe, err = os.Executable(); err != nil {
				return nil, nil, nil, nil, err
			}
		}
		cmd = exec.Command(exe, "ext-host", c.Path)
	default:
		cmd = exec.Command(c.Command, c.Args...)
	}
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "RHO_EXTENSION=1", "RHO_EXTENSION_DIR="+c.Dir)
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, nil, err
	}
	kill := func() {
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	return stdout, stdin, stderr, kill, nil
}
