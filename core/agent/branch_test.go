package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
	"github.com/NDari/rho/core/session"
)

// newFileAgent is newTestAgent with a session stored in a file, so tests
// can reopen it.
func newFileAgent(t *testing.T, fp *fake.Provider) (*Agent, *recorder) {
	t.Helper()
	a, rec, dir := newTestAgent(t, fp)
	if err := a.SetSession(session.New(t.TempDir(), dir, "test")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Session().Close() }) // Windows cannot remove open files
	return a, rec
}

// texts returns the role and text of each message, e.g. "user:hi".
func texts(msgs []provider.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, string(m.Role)+":"+m.TextContent())
	}
	return out
}

// userEntry finds the id of the user message with the given text on the
// current branch.
func userEntry(t *testing.T, s *session.Session, text string) string {
	t.Helper()
	cv := s.BuildContext()
	for i, m := range cv.Messages {
		if m.Role == provider.RoleUser && m.TextContent() == text {
			return cv.EntryIDs[i]
		}
	}
	t.Fatalf("no user message %q on the current branch", text)
	return ""
}

func reopen(t *testing.T, s *session.Session) *session.Session {
	t.Helper()
	if s.Path() == "" {
		t.Fatal("session has no file")
	}
	r, err := session.Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func runTurn(t *testing.T, a *Agent, text string) {
	t.Helper()
	if err := a.Run(context.Background(), provider.UserText(text)); err != nil {
		t.Fatalf("turn %q: %v", text, err)
	}
}

func TestRewindToBranchesAndPersists(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "one"}, fake.Step{Text: "two"}, fake.Step{Text: "two again"})
	a, rec := newFileAgent(t, fp)
	runTurn(t, a, "first")
	runTurn(t, a, "second")
	sess := a.Session()
	second := userEntry(t, sess, "second")

	got, err := a.RewindTo(second)
	if err != nil {
		t.Fatal(err)
	}
	if got != "second" {
		t.Errorf("RewindTo returned %q, want the message text %q", got, "second")
	}
	want := []string{"user:first", "assistant:one"}
	if m := texts(sess.BuildContext().Messages); !slices.Equal(m, want) {
		t.Errorf("after rewind: %v, want %v", m, want)
	}
	if !slices.Contains(rec.types(), events.SessionChanged{}.Type()) {
		t.Errorf("no session_changed event: %v", rec.types())
	}

	// Continuing after the rewind creates a sibling branch.
	runTurn(t, a, "second, reworded")
	want = []string{"user:first", "assistant:one", "user:second, reworded", "assistant:two again"}
	if m := texts(sess.BuildContext().Messages); !slices.Equal(m, want) {
		t.Errorf("new branch: %v, want %v", m, want)
	}
	parent := sess.PathTo(second)[len(sess.PathTo(second))-2].ID
	if n := len(sess.Children(parent)); n != 2 {
		t.Errorf("rewound entry has %d children, want 2 branches", n)
	}

	r := reopen(t, sess)
	if m := texts(r.BuildContext().Messages); !slices.Equal(m, want) {
		t.Errorf("reopened: %v, want %v", m, want)
	}
	if _, ok := r.Get(second); !ok {
		t.Error("the abandoned branch was lost on reopen")
	}
}

func TestRewindToRejects(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "reply"})
	a, _ := newFileAgent(t, fp)
	runTurn(t, a, "hi")
	cv := a.Session().BuildContext()
	assistant := cv.EntryIDs[1]

	tests := []struct {
		name    string
		id      string
		wantErr error
		wantMsg string
	}{
		{"unknown entry", "nope", session.ErrNoEntry, ""},
		{"assistant message", assistant, nil, "can only rewind to a user message"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaf := a.Session().Leaf()
			text, err := a.RewindTo(tt.id)
			if err == nil {
				t.Fatalf("RewindTo(%q) = %q, want an error", tt.id, text)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("err = %v, want %q", err, tt.wantMsg)
			}
			if got := a.Session().Leaf(); got != leaf {
				t.Errorf("leaf moved from %q to %q on a failed rewind", leaf, got)
			}
		})
	}
}

func TestCheckoutStartAndBack(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "one"}, fake.Step{Text: "two"})
	a, _ := newFileAgent(t, fp)
	runTurn(t, a, "first")
	runTurn(t, a, "second")
	sess := a.Session()
	leaf := sess.Leaf()

	if err := a.Checkout(""); err != nil {
		t.Fatal(err)
	}
	if n := len(sess.BuildContext().Messages); n != 0 {
		t.Errorf("checkout of the start left %d messages", n)
	}
	if n := len(reopen(t, sess).BuildContext().Messages); n != 0 {
		t.Errorf("reopened after checkout of the start: %d messages", n)
	}

	if err := a.Checkout(leaf); err != nil {
		t.Fatal(err)
	}
	want := []string{"user:first", "assistant:one", "user:second", "assistant:two"}
	if m := texts(reopen(t, sess).BuildContext().Messages); !slices.Equal(m, want) {
		t.Errorf("reopened after checkout back: %v, want %v", m, want)
	}
}

func TestCheckoutAdoptsSessionModel(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "from a"}, fake.Step{Text: "from b"})
	a, _ := newFileAgent(t, fp)
	if err := a.SetModel("fake/a"); err != nil {
		t.Fatal(err)
	}
	runTurn(t, a, "q1")
	onA := a.Session().Leaf()
	if err := a.SetModel("fake/b"); err != nil {
		t.Fatal(err)
	}
	runTurn(t, a, "q2")
	if got := fp.Requests[1].Model; got != "b" {
		t.Errorf("second request model = %q, want %q", got, "b")
	}

	if err := a.Checkout(onA); err != nil {
		t.Fatal(err)
	}
	if got := a.Model().ID; got != "fake/a" {
		t.Errorf("model after checkout = %q, want the branch's model fake/a", got)
	}
}

func TestCheckoutWhileBusy(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "partial", Wait: true})
	a, _ := newFileAgent(t, fp)
	if err := a.Prompt(provider.UserText("hi")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Cancel(); a.Wait() })
	for !a.Busy() {
		time.Sleep(time.Millisecond)
	}
	if err := a.Checkout(""); !errors.Is(err, ErrBusy) {
		t.Errorf("Checkout while busy = %v, want ErrBusy", err)
	}
}

func TestLabelSetClearPersist(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "reply"})
	a, _ := newFileAgent(t, fp)
	runTurn(t, a, "hi")
	cv := a.Session().BuildContext()
	user, assistant := cv.EntryIDs[0], cv.EntryIDs[1]

	steps := []struct {
		id, label string
	}{
		{user, "start"},
		{assistant, "answer"},
		{assistant, "renamed"},
		{user, ""}, // clears
	}
	for _, s := range steps {
		if err := a.Label(s.id, s.label); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{assistant: "renamed"}
	for name, labels := range map[string]map[string]string{
		"live":     a.Session().Labels(),
		"reopened": reopen(t, a.Session()).Labels(),
	} {
		if len(labels) != len(want) || labels[assistant] != want[assistant] {
			t.Errorf("%s labels = %v, want %v", name, labels, want)
		}
	}
	// Labels are metadata: they do not move the leaf or add messages.
	if got := a.Session().Leaf(); got != assistant {
		t.Errorf("leaf = %q after labelling, want %q", got, assistant)
	}
	if n := len(a.Session().BuildContext().Messages); n != 2 {
		t.Errorf("%d messages after labelling, want 2", n)
	}
}

func TestSummarizeAside(t *testing.T) {
	var summaryReq provider.Request
	fp := fake.New("fake",
		fake.Step{Text: "main answer"},
		fake.Step{Text: "aside answer"},
		fake.Step{Text: "  - use the cache  \n", Usage: provider.Usage{InputTokens: 1000, OutputTokens: 100},
			Check: func(r provider.Request) { summaryReq = r }},
	)
	a, _ := newFileAgent(t, fp)
	cfg := a.Config()
	cfg.Models[0].Pricing = &config.Pricing{Input: 1, Output: 10}
	a.SetConfig(cfg)

	runTurn(t, a, "main question")
	mainLeaf := a.Session().Leaf()
	if err := a.StartAside(); err != nil {
		t.Fatal(err)
	}
	runTurn(t, a, "side question")

	note, err := a.SummarizeAside(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if note != "- use the cache" {
		t.Errorf("note = %q, want the trimmed model text", note)
	}
	// Only the side conversation is summarized.
	if len(summaryReq.Messages) != 1 {
		t.Fatalf("summary request has %d messages, want 1 transcript", len(summaryReq.Messages))
	}
	tr := summaryReq.Messages[0].TextContent()
	if !strings.Contains(tr, "side question") || !strings.Contains(tr, "aside answer") || strings.Contains(tr, "main question") {
		t.Errorf("summary transcript should hold only the aside:\n%s", tr)
	}
	if !strings.Contains(tr, "side conversation") {
		t.Errorf("summary request lacks the aside instructions:\n%s", tr)
	}
	// 1000 input at $1/M + 100 output at $10/M.
	if got, want := a.Cost(), 0.002; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("cost = %v, want %v counted for the summary", got, want)
	}
	// Summarizing does not change the session.
	if !a.Session().BuildContext().Aside {
		t.Error("SummarizeAside ended the aside")
	}

	if err := a.EndAside(note); err != nil {
		t.Fatal(err)
	}
	cv := reopen(t, a.Session()).BuildContext()
	if cv.Aside {
		t.Error("reopened session still in the aside")
	}
	want := []string{"user:main question", "assistant:main answer", "user:[Note carried back from a side conversation]\n- use the cache"}
	if m := texts(cv.Messages); !slices.Equal(m, want) {
		t.Errorf("reopened main branch: %q, want %q", m, want)
	}
	if got := cv.EntryIDs[1]; got != mainLeaf {
		t.Errorf("main branch continues from %q, want %q", got, mainLeaf)
	}
}

func TestSummarizeAsideErrors(t *testing.T) {
	provErr := &provider.Error{Kind: provider.ErrAuth, StatusCode: 401, Message: "bad key"}
	tests := []struct {
		name    string
		steps   []fake.Step
		aside   bool
		sideMsg bool
		want    string
		wantIs  error
	}{
		{name: "not in an aside", steps: []fake.Step{{Text: "a"}}, want: "not in a side conversation"},
		{name: "empty aside", steps: []fake.Step{{Text: "a"}}, aside: true, want: "side conversation is empty"},
		{name: "provider error", steps: []fake.Step{{Text: "a"}, {Text: "b"}, {Err: provErr}}, aside: true, sideMsg: true, wantIs: provErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fp := fake.New("fake", tt.steps...)
			a, _ := newFileAgent(t, fp)
			runTurn(t, a, "main")
			if tt.aside {
				if err := a.StartAside(); err != nil {
					t.Fatal(err)
				}
			}
			if tt.sideMsg {
				runTurn(t, a, "side")
			}
			requests := len(fp.Requests)
			note, err := a.SummarizeAside(context.Background())
			if err == nil {
				t.Fatalf("SummarizeAside = %q, want an error", note)
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("err = %v, want %v", err, tt.wantIs)
			}
			if tt.wantIs == nil && len(fp.Requests) != requests {
				t.Error("a request was sent although there was nothing to summarize")
			}
		})
	}
}
