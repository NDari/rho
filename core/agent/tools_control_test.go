package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
)

func toolNames(r provider.Request) string {
	var names []string
	for _, t := range r.Tools {
		names = append(names, t.Name)
	}
	return strings.Join(names, ",")
}

func TestSetActiveToolsForPlanMode(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Calls: []fake.Call{{ID: "w", Name: "write", Args: map[string]string{"path": "x.txt", "content": "x"}}}, Check: func(r provider.Request) {
			if got := toolNames(r); got != "read,grep,find,ls" {
				t.Errorf("plan mode tools = %s", got)
			}
			if strings.Contains(r.System, "- write:") || strings.Contains(r.System, "- bash:") {
				t.Error("the system prompt still lists tools that are not offered")
			}
		}},
		fake.Step{Text: "planned"},
		fake.Step{Text: "acting", Check: func(r provider.Request) {
			if got := toolNames(r); got != "read,write,edit,bash,grep,find,ls" { // the test agent's configured set
				t.Errorf("tools after reset = %s", got)
			}
		}},
	)
	a, _, _ := newTestAgent(t, fp)
	a.cfg.Tools.Disabled = []string{"powershell-not-a-tool"}
	if err := a.SetActiveTools([]string{"read", "grep", "find", "ls"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), provider.UserText("plan")); err != nil {
		t.Fatal(err)
	}
	cv := a.Session().BuildContext()
	res := cv.Messages[2].Content[0]
	if !res.IsError || !strings.Contains(res.Content[0].Text, "Unknown tool") {
		t.Errorf("a call to a tool that was not offered ran: %+v", res)
	}
	if err := a.SetActiveTools(nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), provider.UserText("act")); err != nil {
		t.Fatal(err)
	}

	if err := a.SetActiveTools([]string{"read", "nope"}); err == nil {
		t.Error("an unknown tool should be refused")
	}
	a.cfg.Tools.Disabled = []string{"bash"}
	if err := a.SetActiveTools([]string{"bash"}); err == nil {
		t.Error("a disabled tool must stay off")
	}
}

func TestBeforeRequestNarrowsTools(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Calls: []fake.Call{{ID: "b", Name: "bash", Args: map[string]string{"command": "echo hi"}}}, Check: func(r provider.Request) {
			if got := toolNames(r); got != "read" {
				t.Errorf("narrowed tools = %s", got)
			}
			if strings.Contains(r.System, "- bash:") {
				t.Error("the rebuilt prompt still lists bash")
			}
		}},
		fake.Step{Text: "done"},
	)
	a, _, _ := newTestAgent(t, fp)
	a.SetHooks(testHooks{tools: func(offered []string) []string {
		if !strings.Contains(strings.Join(offered, ","), "bash") {
			t.Errorf("offered = %v", offered)
		}
		return []string{"read"}
	}})
	if err := a.Run(context.Background(), provider.UserText("go")); err != nil {
		t.Fatal(err)
	}
	res := a.Session().BuildContext().Messages[2].Content[0]
	if !res.IsError {
		t.Errorf("bash ran although the request did not offer it: %+v", res)
	}
}
