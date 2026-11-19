// Package agent implements the agent loop: it sends the conversation to the
// model, executes tool calls, and records everything in the session.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NDari/rho/core/compact"
	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/prompt"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/session"
	"github.com/NDari/rho/core/tools"
)

// Hooks let extensions observe and modify the loop. All methods may be
// called from the agent goroutine.
type Hooks interface {
	// BeforeToolCall may replace the arguments or block the call by
	// returning a non-empty reason.
	BeforeToolCall(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, string)
	// AfterToolCall may modify the result.
	AfterToolCall(ctx context.Context, name string, args json.RawMessage, res tools.Result) tools.Result
	// TurnEnd is called when control returns to the user.
	TurnEnd(ctx context.Context)
	// BeforeCompaction may supply its own summary.
	BeforeCompaction(ctx context.Context, msgs []provider.Message) (string, bool)
}

// ProviderSource returns providers by name.
type ProviderSource interface {
	Get(name string) (provider.Provider, error)
}

// Options configure an Agent.
type Options struct {
	Config    config.Config
	Paths     config.Paths
	Session   *session.Session
	Providers ProviderSource
	Model     config.Model
	Tools     []tools.Tool
	Skills    []prompt.Skill
	Sink      events.Sink
	Hooks     Hooks
}

// ErrBusy is returned when a command needs the agent to be idle.
var ErrBusy = errors.New("the agent is busy")

// ErrNothingToCompact is returned when the conversation is too short to
// compact further.
var ErrNothingToCompact = errors.New("nothing to compact yet")

// Agent runs turns. Its methods are safe for concurrent use.
type Agent struct {
	mu        sync.Mutex
	cfg       config.Config
	paths     config.Paths
	sess      *session.Session
	providers ProviderSource
	model     config.Model
	tools     []tools.Tool
	skills    []prompt.Skill
	sink      events.Sink
	hooks     Hooks

	busy      bool
	cancel    context.CancelFunc
	done      chan struct{}
	steering  []string
	followUps []string
	cost      float64
	lastErr   error
}

// New creates an agent.
func New(o Options) *Agent {
	a := &Agent{
		cfg:       o.Config,
		paths:     o.Paths,
		sess:      o.Session,
		providers: o.Providers,
		model:     o.Model,
		tools:     o.Tools,
		skills:    o.Skills,
		sink:      o.Sink,
		hooks:     o.Hooks,
	}
	if a.sink == nil {
		a.sink = func(events.Event) {}
	}
	a.adoptSessionModel()
	c := a.sessionCost()
	a.mu.Lock()
	a.cost = c
	a.mu.Unlock()
	return a
}

func (a *Agent) emit(e events.Event) { a.sink(e) }

// SetSink replaces the event sink.
func (a *Agent) SetSink(s events.Sink) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sink = s
}

// SetHooks replaces the hooks.
func (a *Agent) SetHooks(h Hooks) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hooks = h
}

// SetTools replaces the tool set (e.g. after extensions reload).
func (a *Agent) SetTools(ts []tools.Tool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tools = ts
}

// SetSkills replaces the skills list.
func (a *Agent) SetSkills(s []prompt.Skill) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.skills = s
}

// Tools returns the full tool set.
func (a *Agent) Tools() []tools.Tool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tools
}

// Session returns the current session.
func (a *Agent) Session() *session.Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sess
}

// Model returns the active model.
func (a *Agent) Model() config.Model {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

// Config returns the configuration.
func (a *Agent) Config() config.Config { return a.cfg }

// Paths returns the resolved paths.
func (a *Agent) Paths() config.Paths { return a.paths }

// Busy reports whether a turn is running.
func (a *Agent) Busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.busy
}

// Cost returns the accumulated session cost in US dollars.
func (a *Agent) Cost() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cost
}

// SetSession switches to another session (for /new, /resume, /fork).
func (a *Agent) SetSession(s *session.Session) error {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return ErrBusy
	}
	old := a.sess
	a.sess = s
	a.mu.Unlock()
	if old != nil && old != s {
		_ = old.Close()
	}
	a.adoptSessionModel()
	c := a.sessionCost()
	a.mu.Lock()
	a.cost = c
	a.mu.Unlock()
	a.emitSessionChanged()
	return nil
}

func (a *Agent) emitSessionChanged() {
	s := a.Session()
	cv := s.BuildContext()
	a.emit(events.SessionChanged{SessionID: s.ID(), Path: s.Path(), Aside: cv.Aside})
	a.emitUsage(cv, provider.Usage{})
}

// adoptSessionModel switches to the model recorded in the session, if any.
func (a *Agent) adoptSessionModel() {
	cv := a.Session().BuildContext()
	if cv.Model == "" {
		return
	}
	if m, err := a.cfg.ResolveModel(cv.Model); err == nil {
		a.mu.Lock()
		a.model = m
		a.mu.Unlock()
	}
}

// SetModel switches the model for subsequent requests and records it.
func (a *Agent) SetModel(id string) error {
	if a.Busy() {
		return ErrBusy
	}
	m, err := a.cfg.ResolveModel(id)
	if err != nil {
		return err
	}
	if _, err := a.providers.Get(m.Provider); err != nil {
		return err
	}
	if _, err := a.Session().Append(session.Entry{Type: session.TypeModelChange, Provider: m.Provider, Model: m.ID}); err != nil {
		return err
	}
	a.mu.Lock()
	a.model = m
	a.mu.Unlock()
	a.emit(events.ModelChanged{Model: m.ID})
	a.emitUsage(a.Session().BuildContext(), provider.Usage{})
	return nil
}

// Prompt starts a turn in the background.
func (a *Agent) Prompt(msg provider.Message) error {
	ctx, err := a.start()
	if err != nil {
		return err
	}
	go a.runTurn(ctx, msg)
	return nil
}

// Run runs a turn and waits for it to finish. It returns the error that
// ended the turn, if any.
func (a *Agent) Run(ctx context.Context, msg provider.Message) error {
	tctx, err := a.start()
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, a.Cancel)
	defer stop()
	a.runTurn(tctx, msg)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
}

func (a *Agent) start() (context.Context, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return nil, ErrBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.busy = true
	a.cancel = cancel
	a.done = make(chan struct{})
	a.lastErr = nil
	return ctx, nil
}

// Wait blocks until the current turn (if any) finishes.
func (a *Agent) Wait() {
	a.mu.Lock()
	done := a.done
	a.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Cancel aborts the current turn.
func (a *Agent) Cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

// Steer queues a message that interrupts the agent after the current tool.
func (a *Agent) Steer(text string) error {
	a.mu.Lock()
	if !a.busy {
		a.mu.Unlock()
		return errors.New("the agent is idle")
	}
	a.steering = append(a.steering, text)
	a.mu.Unlock()
	a.emitQueue()
	return nil
}

// FollowUp queues a message delivered when the agent finishes.
func (a *Agent) FollowUp(text string) error {
	a.mu.Lock()
	if !a.busy {
		a.mu.Unlock()
		return errors.New("the agent is idle")
	}
	a.followUps = append(a.followUps, text)
	a.mu.Unlock()
	a.emitQueue()
	return nil
}

// TakeQueue removes and returns all queued messages (e.g. to put them back
// in the editor after a cancel).
func (a *Agent) TakeQueue() []string {
	a.mu.Lock()
	out := append(append([]string(nil), a.steering...), a.followUps...)
	a.steering, a.followUps = nil, nil
	a.mu.Unlock()
	a.emitQueue()
	return out
}

func (a *Agent) emitQueue() {
	a.mu.Lock()
	q := events.Queue{Steering: append([]string{}, a.steering...), FollowUps: append([]string{}, a.followUps...)}
	a.mu.Unlock()
	a.emit(q)
}

func (a *Agent) popSteering() []string {
	a.mu.Lock()
	out := a.steering
	a.steering = nil
	a.mu.Unlock()
	if len(out) > 0 {
		a.emitQueue()
	}
	return out
}

func (a *Agent) hasSteering() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.steering) > 0
}

func (a *Agent) popFollowUps() []string {
	a.mu.Lock()
	out := a.followUps
	a.followUps = nil
	a.mu.Unlock()
	if len(out) > 0 {
		a.emitQueue()
	}
	return out
}

func (a *Agent) runTurn(ctx context.Context, first provider.Message) {
	var err error
	defer func() {
		reason := "done"
		switch {
		case ctx.Err() != nil:
			reason = "cancelled"
		case err != nil:
			reason = "error"
			a.emit(events.Error{Message: err.Error()})
		}
		if h := a.getHooks(); h != nil {
			h.TurnEnd(context.Background())
		}
		a.mu.Lock()
		a.busy = false
		a.cancel = nil
		if ctx.Err() == nil {
			a.lastErr = err
		} else {
			a.lastErr = ctx.Err()
		}
		done := a.done
		a.mu.Unlock()
		a.emit(events.TurnEnd{Reason: reason})
		close(done)
	}()
	a.emit(events.TurnStart{})
	if err = a.appendMessage(first); err != nil {
		return
	}
	err = a.loop(ctx)
}

func (a *Agent) getHooks() Hooks {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hooks
}

func (a *Agent) appendMessage(m provider.Message) error {
	e, err := a.Session().AppendMessage(m)
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	a.emit(events.MessageEnd{EntryID: e.ID, Message: m})
	return nil
}

func (a *Agent) loop(ctx context.Context) error {
	overflowRetried := false
	for {
		if err := a.maybeCompact(ctx); err != nil && !errors.Is(err, ErrNothingToCompact) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			a.emit(events.Notice{Level: "warn", Text: "automatic compaction failed: " + err.Error()})
		}
		req, prov, err := a.buildRequest()
		if err != nil {
			return err
		}
		resp, err := a.streamWithRetry(ctx, prov, req)
		if err != nil {
			if ctx.Err() != nil {
				a.recordPartial(resp)
				return ctx.Err()
			}
			if provider.KindOf(err) == provider.ErrContextOverflow && !overflowRetried {
				overflowRetried = true
				a.emit(events.Notice{Level: "warn", Text: "context window exceeded; compacting and retrying"})
				if cerr := a.compact(ctx, "overflow"); cerr == nil {
					continue
				} else if ctx.Err() != nil {
					return ctx.Err()
				}
			}
			return err
		}
		resp.Message.Meta.Model = a.Model().ID
		if err := a.appendMessage(resp.Message); err != nil {
			return err
		}
		a.addCost(resp.Usage)
		a.emitUsage(a.Session().BuildContext(), resp.Usage)

		calls := resp.Message.ToolCalls()
		if len(calls) == 0 {
			if resp.StopReason == provider.StopMaxTokens {
				a.emit(events.Notice{Level: "warn", Text: "the response was cut off at the output token limit"})
			}
			if steer := a.popSteering(); len(steer) > 0 {
				if err := a.appendUserTexts(steer); err != nil {
					return err
				}
				continue
			}
			if fu := a.popFollowUps(); len(fu) > 0 {
				if err := a.appendUserTexts(fu); err != nil {
					return err
				}
				continue
			}
			return nil
		}
		if err := a.runTools(ctx, calls); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if steer := a.popSteering(); len(steer) > 0 {
			if err := a.appendUserTexts(steer); err != nil {
				return err
			}
		}
	}
}

func (a *Agent) appendUserTexts(texts []string) error {
	return a.appendMessage(provider.UserText(strings.Join(texts, "\n\n")))
}

// recordPartial saves the text streamed before a cancel, without tool calls.
func (a *Agent) recordPartial(resp provider.Response) {
	var kept []provider.Block
	for _, b := range resp.Message.Content {
		if b.Type == provider.BlockText || b.Type == provider.BlockThinking {
			kept = append(kept, b)
		}
	}
	if len(kept) == 0 {
		return
	}
	m := resp.Message
	m.Content = kept
	m.Meta.StopReason = provider.StopCancelled
	m.Meta.Model = a.Model().ID
	_ = a.appendMessage(m)
}

func (a *Agent) activeTools(aside bool) []tools.Tool {
	ts := a.Tools()
	if aside {
		return tools.ReadOnlyTools(ts)
	}
	return ts
}

// buildRequest assembles the next model request.
func (a *Agent) buildRequest() (provider.Request, provider.Provider, error) {
	model := a.Model()
	prov, err := a.providers.Get(model.Provider)
	if err != nil {
		return provider.Request{}, nil, err
	}
	cv := a.Session().BuildContext()
	ts := a.activeTools(cv.Aside)
	specs := make([]provider.ToolSpec, len(ts))
	for i, t := range ts {
		specs[i] = t.Spec()
	}
	a.mu.Lock()
	skills := a.skills
	a.mu.Unlock()
	sys, err := prompt.Build(a.paths, prompt.Options{Tools: specs, Skills: skills, Aside: cv.Aside})
	if err != nil {
		return provider.Request{}, nil, err
	}
	return provider.Request{
		Model:           model.APIName(),
		System:          sys.Text,
		Messages:        RepairToolPairs(cv.Messages),
		Tools:           specs,
		MaxTokens:       model.MaxOutput,
		ReasoningEffort: model.ReasoningEffort,
		SupportsImages:  model.SupportsImages,
	}, prov, nil
}

// RepairToolPairs makes sure every tool call has a result and every result
// has a call, which providers require. Gaps can appear after crashes.
func RepairToolPairs(msgs []provider.Message) []provider.Message {
	out := make([]provider.Message, 0, len(msgs))
	pending := map[string]bool{}
	var order []string
	flush := func() {
		if len(order) == 0 {
			return
		}
		for _, id := range order {
			if pending[id] {
				out = append(out, provider.Message{Role: provider.RoleToolResult, Content: []provider.Block{
					provider.ToolResult(id, "The tool call was interrupted and produced no result.", true)}})
			}
		}
		pending = map[string]bool{}
		order = nil
	}
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleToolResult:
			keep := false
			for _, b := range m.Content {
				if b.Type == provider.BlockToolResult && pending[b.CallID] {
					pending[b.CallID] = false
					keep = true
				}
			}
			if keep {
				out = append(out, m)
			}
		case provider.RoleAssistant:
			flush()
			out = append(out, m)
			for _, c := range m.ToolCalls() {
				pending[c.ID] = true
				order = append(order, c.ID)
			}
		default:
			flush()
			out = append(out, m)
		}
	}
	flush()
	return out
}

func (a *Agent) streamWithRetry(ctx context.Context, prov provider.Provider, req provider.Request) (provider.Response, error) {
	maxRetries := a.cfg.MaxRetries
	for attempt := 0; ; attempt++ {
		a.emit(events.MessageStart{Model: a.Model().ID})
		resp, err := prov.Stream(ctx, req, func(e provider.StreamEvent) {
			switch e.Kind {
			case provider.EventTextDelta:
				a.emit(events.TextDelta{Text: e.Text})
			case provider.EventThinkingDelta:
				a.emit(events.ThinkingDelta{Text: e.Text})
			case provider.EventToolCallStart, provider.EventToolCallDelta:
				a.emit(events.ToolCallDelta{Index: e.Index, Name: e.Name})
			}
		})
		if err == nil || ctx.Err() != nil {
			return resp, err
		}
		if provider.KindOf(err) != provider.ErrTransient || attempt >= maxRetries {
			return resp, err
		}
		delay := time.Duration(1<<attempt) * 2 * time.Second
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		a.emit(events.Retry{Attempt: attempt + 1, Max: maxRetries, Delay: delay, Error: err.Error()})
		select {
		case <-ctx.Done():
			return resp, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func (a *Agent) runTools(ctx context.Context, calls []provider.Block) error {
	cv := a.Session().BuildContext()
	ts := a.activeTools(cv.Aside)
	byName := map[string]tools.Tool{}
	for _, t := range ts {
		byName[t.Spec().Name] = t
	}
	model := a.Model()
	env := &tools.Env{
		Cwd:            a.paths.Cwd,
		SupportsImages: model.SupportsImages,
		BashTimeout:    a.cfg.Bash.Timeout,
		BashMaxTimeout: a.cfg.Bash.MaxTimeout,
		Shell:          a.cfg.Bash.Shell,
	}
	for _, call := range calls {
		var res tools.Result
		var dur time.Duration
		a.emit(events.ToolStart{ID: call.ID, Name: call.Name, Args: call.Args})
		switch {
		case ctx.Err() != nil:
			res = tools.Result{Content: []provider.Block{provider.Text("Cancelled by the user.")}, IsError: true}
		case a.hasSteering():
			res = tools.Result{Content: []provider.Block{provider.Text("Skipped: the user interrupted with a new message.")}, IsError: true}
		default:
			start := time.Now()
			res = a.execute(ctx, byName, call, env, cv.Aside)
			dur = time.Since(start)
		}
		block := provider.Block{Type: provider.BlockToolResult, CallID: call.ID, Content: res.Content, IsError: res.IsError}
		a.emit(events.ToolEnd{ID: call.ID, Name: call.Name, Result: block, Display: res.Display, Duration: dur})
		if err := a.appendMessage(provider.Message{Role: provider.RoleToolResult, Content: []provider.Block{block}}); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) execute(ctx context.Context, byName map[string]tools.Tool, call provider.Block, env *tools.Env, aside bool) tools.Result {
	t, ok := byName[call.Name]
	if !ok {
		if aside {
			return tools.Result{Content: []provider.Block{provider.Text(fmt.Sprintf("Tool %q is not available in a side conversation (read-only).", call.Name))}, IsError: true}
		}
		return tools.Result{Content: []provider.Block{provider.Text(fmt.Sprintf("Unknown tool %q.", call.Name))}, IsError: true}
	}
	args := call.Args
	hooks := a.getHooks()
	if hooks != nil {
		newArgs, reason := hooks.BeforeToolCall(ctx, call.Name, args)
		if reason != "" {
			return tools.Result{Content: []provider.Block{provider.Text("Blocked: " + reason)}, IsError: true}
		}
		if len(newArgs) > 0 {
			args = newArgs
		}
	}
	res := t.Execute(ctx, env, args, func(s string) {
		a.emit(events.ToolOutput{ID: call.ID, Text: s})
	})
	if hooks != nil {
		res = hooks.AfterToolCall(ctx, call.Name, args, res)
	}
	return res
}

// Compact compacts the conversation now.
func (a *Agent) Compact(ctx context.Context) error {
	tctx, err := a.start()
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, a.Cancel)
	defer stop()
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.cancel = nil
		done := a.done
		a.mu.Unlock()
		close(done)
	}()
	return a.compact(tctx, "manual")
}

func (a *Agent) maybeCompact(ctx context.Context) error {
	cv := a.Session().BuildContext()
	tokens := a.EstimateContext(cv)
	model := a.Model()
	if !compact.ShouldCompact(tokens, model.ContextWindow, a.cfg.Compaction.Reserve) {
		return nil
	}
	return a.compact(ctx, "auto")
}

func (a *Agent) compact(ctx context.Context, reason string) error {
	sess := a.Session()
	cv := sess.BuildContext()
	before := a.EstimateContext(cv)
	model := a.Model()
	keep := compact.KeepTokens(a.cfg.Compaction.KeepRecent, model.ContextWindow, a.cfg.Compaction.Reserve)
	if reason == "overflow" {
		keep /= 2
	}
	cut, ok := compact.FindCut(cv.Messages, cv.EntryIDs, keep)
	if ok && cv.Compaction != nil && cut <= 1 {
		ok = false // only the previous summary would be summarized again
	}
	if !ok {
		return ErrNothingToCompact
	}
	a.emit(events.CompactionStart{Reason: reason, Tokens: before})
	toSummarize := cv.Messages[:cut]
	var summary string
	if h := a.getHooks(); h != nil {
		if s, ok := h.BeforeCompaction(ctx, toSummarize); ok {
			summary = s
		}
	}
	if summary == "" {
		prov, err := a.providers.Get(model.Provider)
		if err != nil {
			a.emit(events.CompactionEnd{TokensBefore: before, Error: err.Error()})
			return err
		}
		s, usage, err := compact.Summarize(ctx, prov, model, toSummarize, compact.Instructions(a.paths))
		a.addCost(usage)
		if err != nil {
			a.emit(events.CompactionEnd{TokensBefore: before, TokensAfter: before, Error: err.Error()})
			return err
		}
		summary = s
	}
	e, err := sess.Append(session.Entry{
		Type:         session.TypeCompaction,
		Summary:      summary,
		FirstKept:    cv.EntryIDs[cut],
		TokensBefore: before,
	})
	if err != nil {
		return err
	}
	ncv := sess.BuildContext()
	after := a.EstimateContext(ncv)
	a.emit(events.CompactionEnd{EntryID: e.ID, TokensBefore: before, TokensAfter: after})
	a.emitUsage(ncv, provider.Usage{})
	return nil
}

// EstimateContext estimates the tokens the context will use: the last
// reported usage plus a rough estimate for newer messages.
func (a *Agent) EstimateContext(cv session.Context) int {
	sess := a.Session()
	var compactionTS time.Time
	if cv.Compaction != nil {
		compactionTS = cv.Compaction.TS
	}
	for i := len(cv.Messages) - 1; i >= 0; i-- {
		m := cv.Messages[i]
		if m.Role != provider.RoleAssistant || m.Meta.Usage == nil || m.Meta.Usage.Total() == 0 {
			continue
		}
		if cv.EntryIDs[i] == "" {
			continue
		}
		e, ok := sess.Get(cv.EntryIDs[i])
		if !ok || e.TS.Before(compactionTS) {
			break
		}
		total := m.Meta.Usage.Total()
		for _, later := range cv.Messages[i+1:] {
			total += later.EstimateTokens()
		}
		return total
	}
	total := 1500 // system prompt and tool definitions, roughly
	for _, m := range cv.Messages {
		total += m.EstimateTokens()
	}
	return total
}

func (a *Agent) emitUsage(cv session.Context, u provider.Usage) {
	model := a.Model()
	a.emit(events.Usage{
		Usage:         u,
		ContextTokens: a.EstimateContext(cv),
		ContextWindow: model.ContextWindow,
		SessionCost:   a.Cost(),
	})
}

func (a *Agent) addCost(u provider.Usage) {
	c := cost(a.Model(), u)
	a.mu.Lock()
	a.cost += c
	a.mu.Unlock()
}

func cost(m config.Model, u provider.Usage) float64 {
	if m.Pricing == nil {
		return 0
	}
	p := m.Pricing
	uncached := u.InputTokens - u.CacheReadTokens - u.CacheWriteTokens
	if uncached < 0 {
		uncached = 0
	}
	return (float64(uncached)*p.Input +
		float64(u.CacheReadTokens)*p.CacheRead +
		float64(u.CacheWriteTokens)*p.CacheWrite +
		float64(u.OutputTokens)*p.Output) / 1e6
}

// sessionCost sums the cost of all assistant messages in the session.
func (a *Agent) sessionCost() float64 {
	total := 0.0
	for _, e := range a.Session().Entries() {
		if e.Type != session.TypeMessage || e.Message == nil || e.Message.Meta.Usage == nil {
			continue
		}
		m, err := a.cfg.ResolveModel(e.Message.Meta.Model)
		if err != nil {
			continue
		}
		total += cost(m, *e.Message.Meta.Usage)
	}
	return total
}
