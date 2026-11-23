package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/NDari/rho/core/provider"
)

// allEvents has one populated value of every event type. A new event type
// must be added here so its JSON form is checked.
func allEvents() []Event {
	code := 2
	return []Event{
		TurnStart{},
		TurnEnd{Reason: "cancelled", Unanswered: "hi", CancelledBy: ByUser},
		MessageStart{Model: "m"},
		TextDelta{Text: "t"},
		ThinkingDelta{Text: "th"},
		ToolCallDelta{Index: 1, Name: "read"},
		MessageEnd{EntryID: "e1", Message: provider.UserText("hello")},
		ToolStart{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
		ToolOutput{ID: "c1", Text: "out"},
		ToolEnd{ID: "c1", Name: "bash", Result: provider.Text("done"), Display: &Display{Kind: "text", Text: "done", ExitCode: &code}, Duration: 3 * time.Millisecond},
		Usage{Usage: provider.Usage{InputTokens: 10, OutputTokens: 2}, ContextTokens: 12, ContextWindow: 100, SessionCost: 0.5},
		Retry{Attempt: 1, Max: 3, Delay: time.Second, Error: "busy"},
		Error{Message: "boom"},
		CompactionStart{Reason: "auto", Tokens: 900},
		CompactionEnd{EntryID: "e2", TokensBefore: 900, TokensAfter: 100},
		Notice{Level: "warn", Text: "careful"},
		Queue{Steering: []string{"s"}, FollowUps: []string{"f"}},
		Status{Key: "k", Text: "v"},
		ModelChanged{Model: "m2"},
		SessionChanged{SessionID: "s", Path: "/p", Aside: true},
	}
}

func TestEventTypesAreStableAndUnique(t *testing.T) {
	// The type names are part of the JSON output scripts depend on.
	want := map[string]string{
		"TurnStart":       "turn_start",
		"TurnEnd":         "turn_end",
		"MessageStart":    "message_start",
		"TextDelta":       "text_delta",
		"ThinkingDelta":   "thinking_delta",
		"ToolCallDelta":   "tool_call_delta",
		"MessageEnd":      "message_end",
		"ToolStart":       "tool_start",
		"ToolOutput":      "tool_output",
		"ToolEnd":         "tool_end",
		"Usage":           "usage",
		"Retry":           "retry",
		"Error":           "error",
		"CompactionStart": "compaction_start",
		"CompactionEnd":   "compaction_end",
		"Notice":          "notice",
		"Queue":           "queue",
		"Status":          "status",
		"ModelChanged":    "model_changed",
		"SessionChanged":  "session_changed",
	}
	snake := regexp.MustCompile(`^[a-z]+(_[a-z]+)*$`)
	seen := map[string]string{}
	for _, e := range allEvents() {
		name := reflect.TypeOf(e).Name()
		typ := e.Type()
		if w, ok := want[name]; !ok {
			t.Errorf("%s: not in the table of expected type names", name)
		} else if typ != w {
			t.Errorf("%s.Type() = %q, want %q", name, typ, w)
		}
		if !snake.MatchString(typ) {
			t.Errorf("%s.Type() = %q is not snake_case", name, typ)
		}
		if other, dup := seen[typ]; dup {
			t.Errorf("%s and %s share type %q", name, other, typ)
		}
		seen[typ] = name
	}
	if len(seen) != len(want) {
		t.Errorf("allEvents has %d types, want %d", len(seen), len(want))
	}
}

func TestMarshalEveryEvent(t *testing.T) {
	for _, e := range allEvents() {
		t.Run(e.Type(), func(t *testing.T) {
			data, err := Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(data, &obj); err != nil {
				t.Fatalf("invalid JSON %s: %v", data, err)
			}
			var typ string
			if err := json.Unmarshal(obj["type"], &typ); err != nil || typ != e.Type() {
				t.Errorf("type field = %s, want %q", obj["type"], e.Type())
			}
			// "type" comes first, so line-oriented readers can dispatch on
			// a prefix.
			if prefix := fmt.Sprintf(`{"type":%q`, e.Type()); string(data[:len(prefix)]) != prefix {
				t.Errorf("JSON %s does not start with %s", data, prefix)
			}
			// Every field of the event survives next to the type: decoding
			// the line back into the event's type gives the same value.
			back := reflect.New(reflect.TypeOf(e))
			if err := json.Unmarshal(data, back.Interface()); err != nil {
				t.Fatal(err)
			}
			if got := back.Elem().Interface(); !reflect.DeepEqual(got, e) {
				t.Errorf("round trip:\n got %#v\nwant %#v", got, e)
			}
			// Marshal adds only the type field.
			plain, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(plain, &fields); err != nil {
				t.Fatal(err)
			}
			if _, clash := fields["type"]; clash {
				t.Errorf("%T has its own type field, which Marshal would duplicate", e)
			}
			if len(obj) != len(fields)+1 {
				t.Errorf("Marshal has %d fields, want %d plus type", len(obj), len(fields))
			}
		})
	}
}

func TestMarshalFieldNames(t *testing.T) {
	tests := []struct {
		ev   Event
		want string
	}{
		{TurnEnd{Reason: "done", Duration: 1500 * time.Millisecond}, `{"type":"turn_end","reason":"done","duration_ns":1500000000}`},
		{Retry{Attempt: 2, Max: 5, Delay: 1500 * time.Millisecond, Error: "429"}, `{"type":"retry","attempt":2,"max":5,"delay_ns":1500000000,"error":"429"}`},
		{CompactionEnd{TokensBefore: 10, TokensAfter: 3, Error: "x"}, `{"type":"compaction_end","tokens_before":10,"tokens_after":3,"error":"x"}`},
		{Queue{}, `{"type":"queue","steering":null,"follow_ups":null}`},
		{ToolCallDelta{}, `{"type":"tool_call_delta","index":0}`},
	}
	for _, tt := range tests {
		got, err := Marshal(tt.ev)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tt.want {
			t.Errorf("Marshal(%T)\n got %s\nwant %s", tt.ev, got, tt.want)
		}
	}
}

// badEvent cannot be encoded.
type badEvent struct{ C chan int }

func (badEvent) Type() string { return "bad" }

func TestMarshalError(t *testing.T) {
	if _, err := Marshal(badEvent{C: make(chan int)}); err == nil {
		t.Error("Marshal of an unencodable event succeeded")
	}
}

func TestCancelled(t *testing.T) {
	c := &Cancelled{By: ByRPCClient}
	if got, want := c.Error(), "cancelled by the RPC client"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{"is context.Canceled", c, context.Canceled, true},
		{"wrapped is context.Canceled", fmt.Errorf("turn: %w", c), context.Canceled, true},
		{"is not DeadlineExceeded", c, context.DeadlineExceeded, false},
		{"is not another error", c, errors.New("cancelled by the RPC client"), false},
	}
	for _, tt := range tests {
		if got := errors.Is(tt.err, tt.target); got != tt.want {
			t.Errorf("%s: errors.Is = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCancelledBy(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() context.Context
		want string
	}{
		{"not cancelled", context.Background, ByUser},
		{"plain cancel", func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, ByUser},
		{"cause names who", func() context.Context {
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(&Cancelled{By: "extension guard"})
			return ctx
		}, "extension guard"},
		{"wrapped cause", func() context.Context {
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(fmt.Errorf("stopping: %w", &Cancelled{By: ByShutdown}))
			return ctx
		}, ByShutdown},
		{"child of a cancelled parent", func() context.Context {
			parent, cancel := context.WithCancelCause(context.Background())
			child, stop := context.WithCancel(parent)
			cancel(&Cancelled{By: ByRPCClient})
			stop() // already cancelled through the parent, so a no-op
			return child
		}, ByRPCClient},
		{"other cause", func() context.Context {
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(errors.New("something else"))
			return ctx
		}, ByUser},
	}
	for _, tt := range tests {
		if got := CancelledBy(tt.ctx()); got != tt.want {
			t.Errorf("%s: CancelledBy = %q, want %q", tt.name, got, tt.want)
		}
	}
	// A context cancelled with a Cancelled cause still reports
	// context.Canceled, so callers that only check Err keep working.
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(&Cancelled{By: ByUser})
	if !errors.Is(ctx.Err(), context.Canceled) || !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Errorf("Err = %v, Cause = %v, want both to match context.Canceled", ctx.Err(), context.Cause(ctx))
	}
}
