package app

import (
	"strings"
	"testing"

	"github.com/NDari/rho/core/provider"
)

func TestForkCopiesLabels(t *testing.T) {
	t.Setenv("RHO_HOME", t.TempDir())
	a, err := New(Options{Cwd: t.TempDir(), Model: "openai/test-model", NoExtensions: true})
	if err != nil {
		t.Fatal(err)
	}
	src := a.Agent.Session()
	var ids []string
	for _, text := range []string{"one", "two", "three"} {
		e, err := src.AppendMessage(provider.UserText(text))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	for id, label := range map[string]string{ids[0]: "start", ids[2]: "beyond"} {
		if err := a.Agent.Label(id, label); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		at       string
		leafWant string // prefix of the label on the fork point
	}{
		{"unlabeled fork point", ids[1], "forked from "},
		{"labeled fork point", ids[0], "start"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dst, err := a.Fork(tc.at)
			if err != nil {
				t.Fatal(err)
			}
			labels := dst.Labels()
			first := dst.PathTo(dst.Leaf())[0]
			if labels[first.ID] != "start" {
				t.Errorf("label on the first entry = %q, want %q", labels[first.ID], "start")
			}
			for _, l := range labels {
				if l == "beyond" {
					t.Error("a label from beyond the fork point was copied")
				}
			}
			if got := labels[dst.Leaf()]; !strings.HasPrefix(got, tc.leafWant) {
				t.Errorf("fork point label = %q, want prefix %q", got, tc.leafWant)
			}
		})
	}
}
