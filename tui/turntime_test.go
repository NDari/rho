package tui

import (
	"testing"
	"time"

	"github.com/NDari/rho/core/events"
)

// TestTurnTimeShown checks the notice that ends each turn.
func TestTurnTimeShown(t *testing.T) {
	tests := []struct {
		name string
		ev   events.TurnEnd
		want string // "" for no notice
	}{
		{"done", events.TurnEnd{Reason: "done", Duration: 83 * time.Second}, "turn took 1m23s"},
		{"error", events.TurnEnd{Reason: "error", Duration: 4 * time.Second}, "turn took 4s"},
		{"cancelled by the user", events.TurnEnd{Reason: "cancelled", CancelledBy: events.ByUser, Duration: 5 * time.Second}, "cancelled after 5s"},
		{"cancelled by an extension", events.TurnEnd{Reason: "cancelled", CancelledBy: "extension guard", Duration: 5 * time.Second}, "cancelled by extension guard after 5s"},
		{"blocked", events.TurnEnd{Reason: "blocked"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			m.handleEvent(tt.ev)
			var got []string
			for _, it := range m.items {
				if n, ok := it.(*noticeItem); ok {
					got = append(got, n.text)
				}
			}
			switch {
			case tt.want == "" && len(got) != 0:
				t.Errorf("notices = %q, want none", got)
			case tt.want != "" && (len(got) != 1 || got[0] != tt.want):
				t.Errorf("notices = %q, want %q", got, tt.want)
			}
		})
	}
}
