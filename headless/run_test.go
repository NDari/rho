package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
)

// syncBuffer is a bytes.Buffer safe for the agent's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newApp creates an App in temp directories whose "openai" provider is a
// fake answering steps. wrap, if not nil, wraps the headless sink.
func newApp(t *testing.T, o Options, wrap func(events.Sink) events.Sink, steps ...fake.Step) (*app.App, *fake.Provider) {
	t.Helper()
	t.Setenv("RHO_HOME", t.TempDir())
	sink := Sink(o)
	if wrap != nil {
		sink = wrap(sink)
	}
	a, err := app.New(app.Options{Cwd: t.TempDir(), Model: "openai/test-model", NoExtensions: true, Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Agent.Session().Close() })
	fp := fake.New("openai", steps...)
	a.Registry.Set("openai", fp)
	return a, fp
}

func setMaxRetries(a *app.App, n int) {
	cfg := a.Agent.Config()
	cfg.MaxRetries = n
	a.Agent.SetConfig(cfg)
}

// jsonLines decodes JSON mode output, failing on any line that is not a
// JSON object with a type.
func jsonLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %q is not JSON: %v", l, err)
		}
		if _, ok := m["type"].(string); !ok {
			t.Fatalf("line %q has no type", l)
		}
		lines = append(lines, m)
	}
	return lines
}

func types(lines []map[string]any) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l["type"].(string))
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestRunJSON(t *testing.T) {
	transient := &provider.Error{Kind: provider.ErrTransient, StatusCode: 503, Message: "overloaded"}
	auth := &provider.Error{Kind: provider.ErrAuth, StatusCode: 401, Message: "bad key"}
	tests := []struct {
		name       string
		steps      []fake.Step
		setup      func(*testing.T, *app.App)
		wantStatus string
		wantAnswer string
		wantSteps  int
		wantErr    bool
		wantTypes  []string // must all appear
		noTypes    []string // must not appear
		check      func(*testing.T, *app.App, []map[string]any)
	}{
		{
			name:       "text reply",
			steps:      []fake.Step{{Text: "hello there", Usage: provider.Usage{InputTokens: 10, OutputTokens: 2}}},
			wantStatus: "done", wantAnswer: "hello there", wantSteps: 1,
			wantTypes: []string{"turn_start", "message_start", "text_delta", "message_end", "turn_end"},
			noTypes:   []string{"error", "tool_start"},
			check: func(t *testing.T, a *app.App, lines []map[string]any) {
				res := lines[len(lines)-1]
				usage, _ := res["usage"].(map[string]any)
				if usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(2) {
					t.Errorf("result usage = %v, want 10 in and 2 out", res["usage"])
				}
				if res["model"] != "openai/test-model" {
					t.Errorf("result model = %v", res["model"])
				}
				if res["session"] == "" || res["session"] != a.Agent.Session().Path() {
					t.Errorf("result session = %v, want %q", res["session"], a.Agent.Session().Path())
				}
				if _, ok := res["error"]; ok {
					t.Errorf("result of a finished turn has an error: %v", res["error"])
				}
			},
		},
		{
			name: "tool call then reply",
			steps: []fake.Step{
				{Text: "let me look", Calls: []fake.Call{{Name: "read", Args: map[string]string{"path": "notes.txt"}}}},
				{Text: "the file says hi"},
			},
			setup: func(t *testing.T, a *app.App) {
				if err := os.WriteFile(filepath.Join(a.Paths.Cwd, "notes.txt"), []byte("hi from the file\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantStatus: "done", wantAnswer: "the file says hi", wantSteps: 2,
			wantTypes: []string{"tool_call_delta", "tool_start", "tool_end", "turn_end"},
			check: func(t *testing.T, a *app.App, lines []map[string]any) {
				for _, l := range lines {
					if l["type"] == "tool_end" {
						data, _ := json.Marshal(l["result"])
						var res provider.Block
						if err := json.Unmarshal(data, &res); err != nil {
							t.Fatal(err)
						}
						if res.IsError || len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "hi from the file") {
							t.Errorf("read result = %+v, want the file's content", res)
						}
						return
					}
				}
				t.Error("no tool_end event")
			},
		},
		{
			name:       "transient error with no retries",
			steps:      []fake.Step{{Err: transient}, {Text: "never sent"}},
			setup:      func(_ *testing.T, a *app.App) { setMaxRetries(a, 0) },
			wantStatus: "error", wantErr: true,
			wantTypes: []string{"error"},
			noTypes:   []string{"retry", "text_delta"},
		},
		{
			name:       "non-retryable error",
			steps:      []fake.Step{{Err: auth}, {Text: "never sent"}},
			wantStatus: "error", wantErr: true,
			wantTypes: []string{"error"},
			noTypes:   []string{"retry"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut syncBuffer
			o := Options{JSON: true, Out: &out, ErrOut: &errOut}
			a, fp := newApp(t, o, nil, tc.steps...)
			if tc.setup != nil {
				tc.setup(t, a)
			}
			err := Run(context.Background(), a, "hi", o)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Run error = %v, want error %v", err, tc.wantErr)
			}
			if tc.wantErr && len(fp.Requests) != 1 {
				t.Errorf("provider got %d requests, want 1", len(fp.Requests))
			}
			lines := jsonLines(t, out.String())
			got := types(lines)
			if got[len(got)-1] != "result" {
				t.Fatalf("last line type = %q, want result; types %v", got[len(got)-1], got)
			}
			for _, w := range tc.wantTypes {
				if !contains(got, w) {
					t.Errorf("no %s event in %v", w, got)
				}
			}
			for _, w := range tc.noTypes {
				if contains(got, w) {
					t.Errorf("unexpected %s event in %v", w, got)
				}
			}
			res := lines[len(lines)-1]
			if res["status"] != tc.wantStatus {
				t.Errorf("status = %v, want %s", res["status"], tc.wantStatus)
			}
			if res["answer"] != tc.wantAnswer {
				t.Errorf("answer = %q, want %q", res["answer"], tc.wantAnswer)
			}
			if tc.wantSteps > 0 && res["steps"] != float64(tc.wantSteps) {
				t.Errorf("steps = %v, want %d", res["steps"], tc.wantSteps)
			}
			if tc.wantErr && res["error"] != err.Error() {
				t.Errorf("result error = %v, want %q", res["error"], err.Error())
			}
			if errOut.String() != "" {
				t.Errorf("JSON mode wrote to stderr: %q", errOut.String())
			}
			if tc.check != nil {
				tc.check(t, a, lines)
			}
		})
	}
}

func TestRunJSONCancelled(t *testing.T) {
	var out syncBuffer
	o := Options{JSON: true, Out: &out, ErrOut: &bytes.Buffer{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel once the model starts streaming; the fake then waits for it.
	wrap := func(s events.Sink) events.Sink {
		return func(e events.Event) {
			s(e)
			if _, ok := e.(events.TextDelta); ok {
				cancel()
			}
		}
	}
	a, _ := newApp(t, o, wrap, fake.Step{Text: "partial", Wait: true})
	err := Run(ctx, a, "hi", o)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	lines := jsonLines(t, out.String())
	res := lines[len(lines)-1]
	if res["type"] != "result" || res["status"] != "cancelled" {
		t.Errorf("last line = %v, want a cancelled result", res)
	}
	if r := TurnResult(a, err); r.Status != "cancelled" || r.Error == "" {
		t.Errorf("TurnResult = %+v, want cancelled with an error", r)
	}
}

func TestRunTransientRetryCancelled(t *testing.T) {
	// The first retry waits seconds, so cancel during the wait: the retry
	// event must be reported and the turn must end as cancelled.
	var out syncBuffer
	o := Options{JSON: true, Out: &out, ErrOut: &bytes.Buffer{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wrap := func(s events.Sink) events.Sink {
		return func(e events.Event) {
			s(e)
			if _, ok := e.(events.Retry); ok {
				cancel()
			}
		}
	}
	transient := &provider.Error{Kind: provider.ErrTransient, StatusCode: 503, Message: "overloaded"}
	a, fp := newApp(t, o, wrap, fake.Step{Err: transient}, fake.Step{Text: "never sent"})
	setMaxRetries(a, 1)
	start := time.Now()
	err := Run(ctx, a, "hi", o)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("cancelling during the retry wait took %s", d)
	}
	if len(fp.Requests) != 1 {
		t.Errorf("provider got %d requests, want 1", len(fp.Requests))
	}
	lines := jsonLines(t, out.String())
	var retry map[string]any
	for _, l := range lines {
		if l["type"] == "retry" {
			retry = l
		}
	}
	if retry == nil {
		t.Fatalf("no retry event in %v", types(lines))
	}
	if retry["attempt"] != float64(1) || retry["max"] != float64(1) {
		t.Errorf("retry event = %v, want attempt 1 of 1", retry)
	}
	if res := lines[len(lines)-1]; res["status"] != "cancelled" {
		t.Errorf("result = %v, want cancelled", res)
	}
}

func TestRunPrint(t *testing.T) {
	tests := []struct {
		name    string
		steps   []fake.Step
		wantOut string
		wantErr bool
	}{
		{"reply", []fake.Step{{Text: "the answer"}}, "the answer\n", false},
		{"no text", []fake.Step{{Thinking: "only thoughts"}}, "", false},
		{"error", []fake.Step{{Err: &provider.Error{Kind: provider.ErrAuth, StatusCode: 401, Message: "bad key"}}}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut syncBuffer
			o := Options{Out: &out, ErrOut: &errOut}
			a, _ := newApp(t, o, nil, tc.steps...)
			err := Run(context.Background(), a, "hi", o)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Run error = %v, want error %v", err, tc.wantErr)
			}
			if out.String() != tc.wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), tc.wantOut)
			}
			if strings.Contains(out.String(), "{") {
				t.Errorf("print mode wrote JSON: %q", out.String())
			}
		})
	}
}

func TestLastAssistantTextStopsAtUser(t *testing.T) {
	// A second turn with no text must not report the first turn's answer.
	var out syncBuffer
	o := Options{Out: &out, ErrOut: &bytes.Buffer{}}
	a, _ := newApp(t, o, nil, fake.Step{Text: "first answer"}, fake.Step{Thinking: "nothing to say"})
	if err := Run(context.Background(), a, "one", o); err != nil {
		t.Fatal(err)
	}
	if got := lastAssistantText(a, false); got != "first answer" {
		t.Fatalf("after turn 1: %q", got)
	}
	if err := Run(context.Background(), a, "two", o); err != nil {
		t.Fatal(err)
	}
	if got := lastAssistantText(a, false); got != "" {
		t.Errorf("after turn 2: %q, want empty", got)
	}
	if got := out.String(); got != "first answer\n" {
		t.Errorf("stdout = %q", got)
	}
}

func TestLastAssistantTextSkipsToolResults(t *testing.T) {
	// The answer is the last assistant text, even when a later assistant
	// message in the turn has none.
	o := Options{Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}
	a, _ := newApp(t, o, nil,
		fake.Step{Text: "reading", Calls: []fake.Call{{Name: "ls", Args: map[string]string{}}}},
		fake.Step{Thinking: "done thinking"},
	)
	if err := Run(context.Background(), a, "hi", o); err != nil {
		t.Fatal(err)
	}
	if got := lastAssistantText(a, false); got != "reading" {
		t.Errorf("lastAssistantText = %q, want %q", got, "reading")
	}
}

func TestRunCutOff(t *testing.T) {
	// The final response was cut off: the result says so, and the earlier
	// "reading" is not reported as the answer.
	tests := []struct {
		name, text string
	}{
		{"thinking only", ""},
		{"partial answer", "The bug is in"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			steps := []fake.Step{
				{Text: "reading", Calls: []fake.Call{{Name: "ls", Args: map[string]string{}}}},
				{Thinking: "still thinking", Text: tc.text, Stop: provider.StopMaxTokens},
			}
			var out syncBuffer
			o := Options{JSON: true, Out: &out, ErrOut: &bytes.Buffer{}}
			a, _ := newApp(t, o, nil, steps...)
			if err := Run(context.Background(), a, "hi", o); err != nil {
				t.Fatal(err)
			}
			lines := jsonLines(t, out.String())
			res := lines[len(lines)-1]
			if res["type"] != "result" || res["status"] != "cut_off" || res["answer"] != tc.text {
				t.Errorf("result = %v, want cut_off with answer %q", res, tc.text)
			}

			var printed syncBuffer
			po := Options{Out: &printed, ErrOut: &bytes.Buffer{}}
			pa, _ := newApp(t, po, nil, steps...)
			if err := Run(context.Background(), pa, "hi", po); err != nil {
				t.Fatal(err)
			}
			want := ""
			if tc.text != "" {
				want = tc.text + "\n"
			}
			if printed.String() != want {
				t.Errorf("print mode = %q, want %q", printed.String(), want)
			}
		})
	}
}

func TestSinkText(t *testing.T) {
	tests := []struct {
		name string
		e    events.Event
		want string
	}{
		{"notice", events.Notice{Level: "warn", Text: "careful"}, "warn: careful\n"},
		{"retry", events.Retry{Attempt: 2, Max: 3, Delay: 4 * time.Second, Error: "503"}, "retrying (2/3) in 4s: 503\n"},
		{"compaction start", events.CompactionStart{Tokens: 1200}, "compacting context (1200 tokens)...\n"},
		{"compaction failed", events.CompactionEnd{Error: "boom"}, "compaction failed: boom\n"},
		{"compaction ok", events.CompactionEnd{}, ""},
		{"text delta", events.TextDelta{Text: "hi"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			Sink(Options{Out: &out, ErrOut: &errOut})(tc.e)
			if errOut.String() != tc.want {
				t.Errorf("stderr = %q, want %q", errOut.String(), tc.want)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", out.String())
			}
		})
	}
}

func TestSinkJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	sink := Sink(Options{JSON: true, Out: &out, ErrOut: &errOut})
	sink(events.Notice{Level: "info", Text: "a"})
	sink(events.TextDelta{Text: "b\nc"})
	lines := jsonLines(t, out.String())
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), out.String())
	}
	if lines[0]["type"] != "notice" || lines[0]["text"] != "a" {
		t.Errorf("line 1 = %v", lines[0])
	}
	if lines[1]["type"] != "text_delta" || lines[1]["text"] != "b\nc" {
		t.Errorf("line 2 = %v", lines[1])
	}
	if errOut.Len() != 0 {
		t.Errorf("JSON sink wrote to stderr: %q", errOut.String())
	}
}

func TestSinkJSONConcurrent(t *testing.T) {
	// Lines written from many goroutines must not interleave.
	var out syncBuffer
	sink := Sink(Options{JSON: true, Out: &out})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				sink(events.TextDelta{Text: strings.Repeat("x", 100)})
			}
		}()
	}
	wg.Wait()
	if n := len(jsonLines(t, out.String())); n != 400 {
		t.Errorf("got %d lines, want 400", n)
	}
}

func TestUI(t *testing.T) {
	var errOut bytes.Buffer
	ui := UI{ErrOut: &errOut}
	ctx := context.Background()
	if ok, err := ui.Confirm(ctx, "title", "proceed?"); ok || err != nil {
		t.Errorf("Confirm = %v, %v; want declined without error", ok, err)
	}
	if v, err := ui.Select(ctx, "pick", []string{"a", "b"}); v != "" || !errors.Is(err, errNoUI) {
		t.Errorf("Select = %q, %v; want errNoUI", v, err)
	}
	if v, err := ui.Input(ctx, "name", "default"); v != "" || !errors.Is(err, errNoUI) {
		t.Errorf("Input = %q, %v; want errNoUI", v, err)
	}
	ui.SetStatus("key", "text")
	ui.Notify("warn", "heads up")
	if got := errOut.String(); got != "warn: heads up\n" {
		t.Errorf("stderr = %q", got)
	}
	// Without a writer, Notify must not panic.
	UI{}.Notify("info", "dropped")
}
