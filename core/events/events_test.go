package events

import (
	"encoding/json"
	"testing"
)

func TestMarshalAddsType(t *testing.T) {
	tests := []struct {
		ev   Event
		want string
	}{
		{TextDelta{Text: "hi"}, `{"type":"text_delta","text":"hi"}`},
		{TurnStart{}, `{"type":"turn_start"}`},
	}
	for _, tt := range tests {
		got, err := Marshal(tt.ev)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tt.want {
			t.Errorf("Marshal(%T) = %s, want %s", tt.ev, got, tt.want)
		}
		if !json.Valid(got) {
			t.Errorf("invalid JSON: %s", got)
		}
	}
}
