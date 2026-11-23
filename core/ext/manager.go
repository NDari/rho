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
	// notes queues notification hooks for notesConn, so a slow extension
	// cannot stall the agent.
	notes     chan note
	notesConn *rpc.Conn
	// waits are the calls to the extension waiting for a reply, and
	// prompts the UI prompts it has open. While a prompt is open the
	// calls' timeouts are paused, so time spent waiting on the user does
	// not count.
	waits   map[*waitingCall]struct{}
	prompts map[*openPrompt]struct{}
	// exited is closed once the process's exit has been handled (status
	// set, restart decided).
	exited chan struct{}
}

type note struct {
	method string
	params any
}

// enqueue sends a notification without waiting. A full queue drops it.
func (e *Extension) enqueue(method string, params any) {
	e.mu.Lock()
	conn := e.conn
	if conn == nil {
		e.mu.Unlock()
		return
	}
	if e.notesConn != conn {
		e.notes, e.notesConn = make(chan note, 256), conn
		go func(ch chan note, c *rpc.Conn) {
			for {
				select {
				case n := <-ch:
					_ = c.Notify(n.method, n.params)
				case <-c.Done():
					return
				}
			}
		}(e.notes, conn)
	}
	ch := e.notes
	e.mu.Unlock()
	select {
	case ch <- note{method, params}:
	default:
		e.log(method + ": dropped, the extension is not keeping up")
	}
}

// waitingCall is a call to the extension waiting for its reply.
type waitingCall struct {
	timer  *time.Timer // nil without a timeout
	left   time.Duration
	since  time.Time
	paused bool
}

func (w *waitingCall) pause() {
	if w.timer != nil && !w.paused && w.timer.Stop() {
		w.paused = true
		w.left -= time.Since(w.since)
	}
}

func (w *waitingCall) resume() {
	if w.paused {
		w.paused = false
		w.since = time.Now()
		w.timer.Reset(max(w.left, 0))
	}
}

// openPrompt is a UI prompt the extension opened. tied prompts were opened
// while a call was waiting and are closed once no call waits any more, so
// a prompt cannot be answered after the call it was for was decided.
type openPrompt struct {
	cancel context.CancelCauseFunc
	tied   bool
	ended  chan struct{} // closed when the frontend has given its answer
}

// promptCloseWait is how long a call that closed prompts waits for the
// frontends to take them down, so that the close reaches them before what
// follows the call (such as turn_end), even with a frontend that ignores
// the close.
const promptCloseWait = time.Second

// call sends a request and waits for the reply. timeout (0 for none)
// limits the time the extension takes, not the time its prompts to the
// user are open.
func (e *Extension) call(ctx context.Context, timeout time.Duration, method string, params, result any) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	w := &waitingCall{left: timeout}
	e.mu.Lock()
	conn := e.conn
	if timeout > 0 {
		w.since = time.Now()
		w.timer = time.AfterFunc(timeout, func() {
			cancel(fmt.Errorf("%s: no reply within %s", method, timeout))
		})
		if len(e.prompts) > 0 {
			w.pause()
		}
	}
	if e.waits == nil {
		e.waits = map[*waitingCall]struct{}{}
	}
	e.waits[w] = struct{}{}
	e.mu.Unlock()
	err := conn.Call(ctx, method, params, result)
	e.endCall(ctx, w, err)
	if cause := context.Cause(ctx); err != nil && cause != nil && errors.Is(err, context.Canceled) {
		return cause
	}
	return err
}

// endCall forgets a call that ended with err (or whose ctx ended) and
// closes the prompts tied to it, giving the reason as their context's
// cause.
func (e *Extension) endCall(ctx context.Context, w *waitingCall, err error) {
	e.mu.Lock()
	delete(e.waits, w)
	if w.timer != nil {
		w.timer.Stop()
	}
	stopped := e.Status != StatusRunning
	var closing []*openPrompt
	if len(e.waits) == 0 {
		for p := range e.prompts {
			if p.tied {
				closing = append(closing, p)
			}
		}
	}
	e.mu.Unlock()
	if len(closing) == 0 {
		return
	}
	// The turn was cancelled (the cause says by whom), the call timed out,
	// the extension stopped, or it replied without waiting for its prompt.
	reason := context.Cause(ctx)
	switch {
	case reason != nil:
	case stopped || errors.Is(err, rpc.ErrClosed):
		reason = fmt.Errorf("extension %s stopped", e.Name)
	default:
		reason = fmt.Errorf("extension %s finished without waiting for the answer", e.Name)
	}
	for _, p := range closing {
		p.cancel(reason)
	}
	wait := time.NewTimer(promptCloseWait)
	defer wait.Stop()
	for _, p := range closing {
		select {
		case <-p.ended:
		case <-wait.C:
			return
		}
	}
}

// prompt registers a UI prompt the extension opened and returns its
// context and a function to call when it is answered. connCtx ends when
// the extension stops. When Rho closes the prompt, context.Cause of the
// returned context says why.
func (e *Extension) prompt(connCtx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(context.Background())
	stop := context.AfterFunc(connCtx, func() { cancel(fmt.Errorf("extension %s stopped", e.Name)) })
	e.mu.Lock()
	p := &openPrompt{cancel: cancel, tied: len(e.waits) > 0, ended: make(chan struct{})}
	if len(e.prompts) == 0 {
		for w := range e.waits {
			w.pause()
		}
	}
	if e.prompts == nil {
		e.prompts = map[*openPrompt]struct{}{}
	}
	e.prompts[p] = struct{}{}
	e.mu.Unlock()
	return ctx, func() {
		e.mu.Lock()
		delete(e.prompts, p)
		if len(e.prompts) == 0 {
			for w := range e.waits {
				w.resume()
			}
		}
		e.mu.Unlock()
		stop()
		cancel(nil)
		close(p.ended)
	}
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
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Status == StatusRunning && slices.Contains(e.Info.Hooks, name)
}

// AgentControl is the subset of the agent extensions may drive.
type AgentControl interface {
	Busy() bool
	Prompt(provider.Message) error
	FollowUp(string) error
	// SetModel switches models; it fails while a turn runs.
	SetModel(id string) error
	// SetActiveTools changes the tools offered from the next request on;
	// nil goes back to the configured set.
	SetActiveTools(names []string) error
	// CancelBy stops the current turn, if any, recording who cancelled
	// it.
	CancelBy(by string)
	// Compact compacts the conversation now; it fails while a turn runs.
	Compact(ctx context.Context) error
	Info() SessionInfo
	// Messages are the messages of the current branch, as the model
	// would see them.
	Messages() []SessionEntry
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
	// OnRestart is called after an extension that crashed was restarted
	// automatically, so the caller can offer its new tools.
	OnRestart func()
}

// Manager discovers, starts and talks to extensions.
type Manager struct {
	o        Options
	trust    *TrustStore
	mu       sync.Mutex
	exts     []*Extension
	progress sync.Map // call id -> func(string)
	warnings []string
	// toolHooks serializes before_tool_call and after_tool_call.
	toolHooks sync.Mutex
	// testTimeout replaces extensions.timeout in tests, which need less
	// than a second.
	testTimeout time.Duration
	// restarted records when each extension (by ID) was last restarted
	// after a crash.
	restarted map[string]time.Time
	// restarting holds a channel per extension ID being restarted, closed
	// when the restart is over.
	restarting map[string]chan struct{}
	// restartDelay replaces the wait before a restart in tests.
	restartDelay time.Duration
}

// Crash restarts: an extension that crashes is started again after
// restartWait, unless it was already restarted within restartWindow, so a
// one-off crash heals itself and a crash loop stops.
const (
	restartWait   = time.Second
	restartWindow = time.Minute
)

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
	if m.testTimeout > 0 {
		return m.testTimeout
	}
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
		{m.o.Paths.Project("extensions"), true},
	}
	seen := map[string]bool{}
	for _, d := range dirs {
		if d.path == "" || seen[d.path] {
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
	stderrDone := make(chan struct{})
	if stderr != nil {
		go func() {
			defer close(stderrDone)
			sc := bufio.NewScanner(stderr)
			for sc.Scan() {
				e.log(sc.Text())
			}
		}()
	} else {
		close(stderrDone)
	}
	e.conn = rpc.NewConn(stdout, stdin, m.handler(e))
	ictx, cancel := context.WithTimeout(ctx, m.timeout())
	defer cancel()
	var res InitializeResult
	err = e.conn.Call(ictx, "initialize", InitializeParams{ProtocolVersion: ProtocolVersion, RhoVersion: m.o.Version, Cwd: m.o.Paths.Cwd, Hooks: SupportedHooks, Methods: SupportedMethods}, &res)
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
	exited := make(chan struct{})
	e.mu.Lock()
	e.exited = exited
	e.mu.Unlock()
	go func() {
		<-e.conn.Done()
		e.mu.Lock()
		wasRunning := e.Status == StatusRunning
		if wasRunning {
			e.Status = StatusFailed
			e.Err = "exited unexpectedly"
		}
		e.mu.Unlock()
		restart := wasRunning && m.mayRestart(e)
		close(exited)
		if !wasRunning {
			return
		}
		// A crashing process's last words (e.g. a panic) are still being
		// read from stderr; give that a moment so the notice includes them.
		select {
		case <-stderrDone:
		case <-time.After(500 * time.Millisecond):
		}
		logs := e.lastLogs(3) // takes e.mu itself
		e.mu.Lock()
		if len(logs) > 0 {
			e.Err += ": " + strings.Join(logs, " | ")
		}
		msg := e.Err
		e.mu.Unlock()
		if restart {
			m.o.Sink(events.Notice{Level: "error", Text: fmt.Sprintf("extension %s crashed (%s); restarting it", e.Name, msg)})
			go m.restart(e)
			return
		}
		m.o.Sink(events.Notice{Level: "error", Text: fmt.Sprintf("extension %s crashed (%s); /reload to restart", e.Name, msg)})
	}()
}

// mayRestart reports whether e may be restarted after a crash, and if so
// records the restart. Project extensions are only restarted while still
// trusted, so a restart never asks the user.
func (m *Manager) mayRestart(e *Extension) bool {
	if e.Project {
		hash, err := HashPath(e.Path)
		if err != nil || !m.trust.Trusted(e.Path, hash) {
			return false
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !slices.Contains(m.exts, e) {
		return false // replaced (e.g. by /reload) or still starting
	}
	if last, ok := m.restarted[e.ID]; ok && time.Since(last) < restartWindow {
		return false
	}
	if m.restarted == nil {
		m.restarted = map[string]time.Time{}
		m.restarting = map[string]chan struct{}{}
	}
	m.restarted[e.ID] = time.Now()
	m.restarting[e.ID] = make(chan struct{})
	return true
}

// awaitRestart waits for a restart of e that is under way and returns the
// running replacement if it still offers tool, or nil. A tool call that arrives while its
// extension restarts (the model answers fast) then reaches the new process
// instead of failing.
func (m *Manager) awaitRestart(ctx context.Context, e *Extension, tool string) *Extension {
	m.mu.Lock()
	ch := m.restarting[e.ID]
	m.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return nil
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.exts {
		if x.ID == e.ID && x != e {
			x.mu.Lock()
			ok := x.Status == StatusRunning && slices.ContainsFunc(x.Info.Tools, func(i ToolInfo) bool { return i.Name == tool })
			x.mu.Unlock()
			if ok {
				return x
			}
		}
	}
	return nil
}

// restart starts a fresh copy of the crashed extension e in its place.
func (m *Manager) restart(e *Extension) {
	defer func() {
		m.mu.Lock()
		if ch := m.restarting[e.ID]; ch != nil {
			close(ch)
			delete(m.restarting, e.ID)
		}
		m.mu.Unlock()
	}()
	wait := restartWait
	if m.restartDelay > 0 {
		wait = m.restartDelay
	}
	time.Sleep(wait)
	m.mu.Lock()
	current := slices.Contains(m.exts, e)
	m.mu.Unlock()
	if !current {
		return
	}
	c := e.Candidate
	c.Name = e.ID
	fresh := &Extension{Candidate: c, ID: e.ID}
	ctx := context.Background()
	m.startOne(ctx, fresh)
	m.mu.Lock()
	i := slices.Index(m.exts, e)
	if i != -1 {
		m.exts[i] = fresh
	}
	m.mu.Unlock()
	if i == -1 {
		stopOne(fresh, StatusStopped)
		return
	}
	m.resolveConflicts()
	fresh.mu.Lock()
	status, errText := fresh.Status, fresh.Err
	fresh.mu.Unlock()
	if status != StatusRunning {
		m.o.Sink(events.Notice{Level: "error", Text: fmt.Sprintf("extension %s could not be restarted: %s; /reload to try again", fresh.Name, errText)})
		return
	}
	if fresh.hasHook(HookSessionStart) {
		_ = fresh.call(ctx, m.timeout(), "hook/"+HookSessionStart, HookParams{}, nil)
	}
	if m.o.OnRestart != nil {
		m.o.OnRestart()
	}
	m.o.Sink(events.Notice{Level: "info", Text: fmt.Sprintf("extension %s restarted", fresh.Name)})
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
			_ = e.call(ctx, m.timeout(), "hook/"+HookSessionEnd, HookParams{}, nil)
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
			_ = fresh.call(ctx, m.timeout(), "hook/"+HookSessionStart, HookParams{}, nil)
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
			pctx, done := e.prompt(ctx)
			ok, err := ui.Confirm(pctx, p.Title, p.Message)
			done()
			return ConfirmResult{Confirmed: ok && err == nil}, nil
		case "ui/select":
			var p SelectParams
			_ = json.Unmarshal(params, &p)
			if ui == nil {
				return ValueResult{Cancelled: true}, nil
			}
			pctx, done := e.prompt(ctx)
			v, err := ui.Select(pctx, p.Title, p.Options)
			done()
			return ValueResult{Value: v, Cancelled: err != nil}, nil
		case "ui/input":
			var p InputParams
			_ = json.Unmarshal(params, &p)
			if ui == nil {
				return ValueResult{Cancelled: true}, nil
			}
			pctx, done := e.prompt(ctx)
			v, err := ui.Input(pctx, p.Title, p.Placeholder)
			done()
			return ValueResult{Value: v, Cancelled: err != nil}, nil
		case "session/info", "session/messages", "agent/set_model", "agent/set_tools", "agent/cancel", "agent/compact":
			m.mu.Lock()
			ag := m.o.Agent
			m.mu.Unlock()
			if ag == nil {
				return nil, errors.New("no agent")
			}
			res, err := agentMethod(ctx, ag, e.Name, method, params)
			if err == nil && method == "agent/set_tools" {
				m.o.Sink(events.Notice{Level: "info", Text: fmt.Sprintf("extension %s set the tools: %s", e.Name, strings.Join(ag.Info().Tools, ", "))})
			}
			return res, err
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

// agentMethod serves the session/ and agent/ requests other than
// session/append_message.
func agentMethod(ctx context.Context, ag AgentControl, name, method string, params json.RawMessage) (any, error) {
	switch method {
	case "session/info":
		return ag.Info(), nil
	case "session/messages":
		return SessionMessagesResult{Messages: ag.Messages()}, nil
	case "agent/set_model":
		var p SetModelParams
		if err := json.Unmarshal(params, &p); err != nil || p.Model == "" {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "model is required"}
		}
		return struct{}{}, ag.SetModel(p.Model)
	case "agent/set_tools":
		var p SetToolsParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "tools must be a list of names"}
			}
		}
		return struct{}{}, ag.SetActiveTools(p.Tools)
	case "agent/cancel":
		ag.CancelBy("extension " + name)
		return struct{}{}, nil
	default: // agent/compact
		return struct{}{}, ag.Compact(ctx)
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
				return e.call(ctx, 10*m.timeout(), "command/run", CommandRunParams{Name: name, Args: args}, nil)
			}
		}
	}
	return fmt.Errorf("unknown command /%s", name)
}

// ToolDisabled reports whether tools.disabled names the tool.
func (m *Manager) ToolDisabled(name string) bool {
	return slices.Contains(m.o.Config.Tools.Disabled, name)
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
			if m.ToolDisabled(t.Name) {
				fmt.Fprintf(&b, "  tool: %s (disabled in tools.disabled)\n", t.Name)
			} else {
				fmt.Fprintf(&b, "  tool: %s\n", t.Name)
			}
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
		_ = e.call(ctx, m.timeout(), "hook/"+hook, p, nil)
	}
}

// BeforeToolCall implements agent.Hooks. Tool hooks are serialized, so an
// extension never handles two at once even when read-only calls run in
// parallel.
func (m *Manager) BeforeToolCall(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, string) {
	m.toolHooks.Lock()
	defer m.toolHooks.Unlock()
	for _, e := range m.running() {
		if !e.hasHook(HookBeforeToolCall) {
			continue
		}
		var res HookResult
		err := e.call(ctx, 10*m.timeout(), "hook/"+HookBeforeToolCall, HookParams{Tool: name, Args: args}, &res)
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

// AfterToolCall implements agent.Hooks; see BeforeToolCall.
func (m *Manager) AfterToolCall(ctx context.Context, name string, args json.RawMessage, res tools.Result) tools.Result {
	m.toolHooks.Lock()
	defer m.toolHooks.Unlock()
	for _, e := range m.running() {
		if !e.hasHook(HookAfterToolCall) {
			continue
		}
		var hr HookResult
		r := toWire(res.Content, res.IsError)
		err := e.call(ctx, m.timeout(), "hook/"+HookAfterToolCall, HookParams{Tool: name, Args: args, Result: &r}, &hr)
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

// UserMessage implements agent.Hooks: each extension in turn may rewrite
// the text or block the message.
func (m *Manager) UserMessage(ctx context.Context, text string) (string, string) {
	for _, e := range m.running() {
		if !e.hasHook(HookUserMessage) {
			continue
		}
		var res HookResult
		err := e.call(ctx, 10*m.timeout(), "hook/"+HookUserMessage, HookParams{Text: text}, &res)
		if err != nil {
			e.log("user_message: " + err.Error())
			continue
		}
		if res.Block != "" {
			return text, res.Block + " (blocked by extension " + e.Name + ")"
		}
		if res.Text != "" {
			text = res.Text
		}
	}
	return text, ""
}

// BeforeRequest implements agent.Hooks: each extension in turn may replace
// the system prompt and messages of one request.
func (m *Manager) BeforeRequest(ctx context.Context, system string, msgs []provider.Message, tools []string, aside bool) (string, []provider.Message, []string) {
	var keep []string // nil: unchanged
	for _, e := range m.running() {
		if !e.hasHook(HookBeforeRequest) {
			continue
		}
		var res HookResult
		err := e.call(ctx, 10*m.timeout(), "hook/"+HookBeforeRequest, HookParams{System: system, Messages: msgs, Tools: tools, Aside: aside}, &res)
		if err != nil {
			e.log("before_request: " + err.Error())
			continue
		}
		if res.System != "" {
			system = res.System
		}
		if len(res.Messages) > 0 {
			msgs = res.Messages
		}
		if len(res.Tools) > 0 {
			// Narrow only: names not offered are ignored.
			narrowed := []string{}
			for _, t := range tools {
				if slices.Contains(res.Tools, t) {
					narrowed = append(narrowed, t)
				}
			}
			tools, keep = narrowed, narrowed
		}
	}
	return system, msgs, keep
}

// MessageEnd implements agent.Hooks: it notifies extensions of a saved
// message without waiting for them.
func (m *Manager) MessageEnd(entryID string, msg provider.Message) {
	m.notifyHook(HookMessageEnd, HookParams{EntryID: entryID, Message: &msg})
}

// ToolEnd implements agent.Hooks: it notifies extensions of a finished
// tool call, with its final result, without waiting for them.
func (m *Manager) ToolEnd(name string, args json.RawMessage, res tools.Result) {
	r := toWire(res.Content, res.IsError)
	m.notifyHook(HookToolEnd, HookParams{Tool: name, Args: args, Result: &r})
}

func (m *Manager) notifyHook(hook string, p HookParams) {
	for _, e := range m.running() {
		if e.hasHook(hook) {
			e.enqueue("hook/"+hook, p)
		}
	}
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
		var hr HookResult
		err := e.call(ctx, 10*m.timeout(), "hook/"+HookBeforeCompaction, HookParams{Transcript: compact.Transcript(msgs, 4000)}, &hr)
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
	running, conn, exited := t.ext.Status == StatusRunning, t.ext.conn, t.ext.exited
	t.ext.mu.Unlock()
	if running && conn != nil && exited != nil {
		select {
		case <-conn.Done():
			// The process died and the exit is being handled.
			select {
			case <-exited:
			case <-ctx.Done():
			}
			running = false
		default:
		}
	}
	if !running {
		fresh := t.m.awaitRestart(ctx, t.ext, t.info.Name)
		if fresh == nil {
			return tools.Result{Content: []provider.Block{provider.Text("extension " + t.ext.Name + " is not running")}, IsError: true}
		}
		t = &extTool{m: t.m, ext: fresh, info: t.info}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	callID := hex.EncodeToString(b[:])
	if progress != nil {
		t.m.progress.Store(callID, progress)
		defer t.m.progress.Delete(callID)
	}
	var res ToolResult
	err := t.ext.call(ctx, 0, "tool/execute", ToolExecuteParams{CallID: callID, Name: t.info.Name, Args: args}, &res)
	if err != nil {
		if ctx.Err() != nil {
			_ = t.ext.conn.Notify("tool/cancel", map[string]string{"call_id": callID})
			return tools.Result{Content: []provider.Block{provider.Text("Cancelled by " + events.CancelledBy(ctx) + ".")}, IsError: true}
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
