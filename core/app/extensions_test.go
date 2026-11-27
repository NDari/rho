package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NDari/rho/core/agent"
	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
	"github.com/NDari/rho/core/tools"
)

// probeExtension offers a session_probe tool that answers with what the
// extension sees through session/info and session/messages.
const probeExtension = `package main

import (
	"encoding/json"

	"rho"
)

func main() {
	rho.Info("probe", "0.1.0")
	rho.RegisterTool(rho.Tool{
		Name:        "session_probe",
		Description: "Reports the session as the extension sees it",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		ReadOnly:    true,
		Execute: func(ctx *rho.ToolContext, args map[string]any) (string, error) {
			info, err := rho.Session.Info()
			if err != nil {
				return "", err
			}
			msgs, err := rho.Session.Messages()
			if err != nil {
				return "", err
			}
			out, err := json.Marshal(map[string]any{"info": info, "messages": len(msgs)})
			return string(out), err
		},
	})
}
`

// extApp is a testApp with the probe extension installed globally and
// started.
func extApp(t *testing.T, project string, steps ...fake.Step) *App {
	t.Helper()
	a := testApp(t, "", project, true, steps...)
	writeFile(t, filepath.Join(a.Paths.Home, "extensions", "probe", "probe.go"), probeExtension)
	a.StartExtensions(context.Background(), quietUI{})
	t.Cleanup(a.StopExtensions)
	return a
}

func toolNames(a *App) []string { return tools.Names(a.Agent.Tools()) }

func extStatus(t *testing.T, a *App, id string) string {
	t.Helper()
	for _, e := range a.Ext.Extensions() {
		if e.ID == id {
			return e.Status
		}
	}
	t.Fatalf("no extension %q", id)
	return ""
}

func TestSessionInfoThroughExtension(t *testing.T) {
	a := extApp(t, "",
		fake.Step{Calls: []fake.Call{{ID: "p1", Name: "session_probe", Args: map[string]any{}}}},
		fake.Step{Text: "probed"})
	if got := extStatus(t, a, "probe"); got != ext.StatusRunning {
		t.Fatalf("probe status = %s: %s", got, a.Ext.Describe())
	}
	if err := a.Agent.Run(context.Background(), provider.UserText("probe the session")); err != nil {
		t.Fatal(err)
	}

	// What the extension saw while its tool ran: the turn is busy and the
	// branch holds the prompt and the assistant's tool call.
	msgs := a.SessionMessages()
	var result string
	for _, m := range msgs {
		if m.Message.Role == provider.RoleToolResult {
			for _, b := range m.Message.Content {
				for _, c := range b.Content {
					result += c.Text
				}
			}
		}
	}
	var seen struct {
		Info     ext.SessionInfo `json:"info"`
		Messages int             `json:"messages"`
	}
	if err := json.Unmarshal([]byte(result), &seen); err != nil {
		t.Fatalf("tool result %q: %v", result, err)
	}
	if !seen.Info.Busy || seen.Messages != 2 || seen.Info.Model != "openai/test-model" || seen.Info.Cwd != a.Paths.Cwd {
		t.Errorf("extension saw %+v", seen)
	}
	if !slices.Contains(seen.Info.Tools, "session_probe") {
		t.Errorf("extension saw tools %v", seen.Info.Tools)
	}

	// After the turn, as the frontends see it.
	info := a.SessionInfo()
	sess := a.Agent.Session()
	if info.ID != sess.ID() || info.File != sess.Path() || info.File == "" || info.Busy || info.Cwd != a.Paths.Cwd {
		t.Errorf("SessionInfo = %+v", info)
	}
	if info.ContextWindow != a.Agent.Model().ContextWindow || info.ContextTokens <= 0 {
		t.Errorf("context %d of %d", info.ContextTokens, info.ContextWindow)
	}
	if seen.Info.ID != info.ID {
		t.Errorf("extension saw session %q, want %q", seen.Info.ID, info.ID)
	}
	cv := sess.BuildContext()
	if len(msgs) != 4 || len(msgs) != len(cv.Messages) {
		t.Fatalf("SessionMessages has %d messages, context %d", len(msgs), len(cv.Messages))
	}
	for i, m := range msgs {
		if m.ID == "" || m.ID != cv.EntryIDs[i] || m.Message.Role != cv.Messages[i].Role {
			t.Errorf("message %d = %s %s, want %s %s", i, m.ID, m.Message.Role, cv.EntryIDs[i], cv.Messages[i].Role)
		}
	}
	if got := msgs[3].Message.TextContent(); got != "probed" {
		t.Errorf("last message = %q", got)
	}
}

func TestSetExtensionEnabled(t *testing.T) {
	a := extApp(t, "")
	if !slices.Contains(toolNames(a), "session_probe") {
		t.Fatalf("tools = %v", toolNames(a))
	}

	note, err := a.SetExtensionEnabled(context.Background(), "probe", false)
	if err != nil || note != "" {
		t.Fatalf("disable: note %q, err %v", note, err)
	}
	if slices.Contains(toolNames(a), "session_probe") {
		t.Errorf("a disabled extension's tool is still offered: %v", toolNames(a))
	}
	if got := extStatus(t, a, "probe"); got != ext.StatusDisabled {
		t.Errorf("status = %s", got)
	}
	if !slices.Contains(a.Config.Extensions.Disabled, "probe") || !slices.Contains(a.Agent.Config().Extensions.Disabled, "probe") {
		t.Errorf("extensions.disabled = %v", a.Config.Extensions.Disabled)
	}
	if file := readFile(t, a.Paths.GlobalFile()); !strings.Contains(file, `"probe"`) {
		t.Errorf("config file does not record the choice:\n%s", file)
	}

	note, err = a.SetExtensionEnabled(context.Background(), "probe", true)
	if err != nil || note != "" {
		t.Fatalf("enable: note %q, err %v", note, err)
	}
	if !slices.Contains(toolNames(a), "session_probe") {
		t.Errorf("tools after enabling = %v", toolNames(a))
	}
	if got := extStatus(t, a, "probe"); got != ext.StatusRunning {
		t.Errorf("status = %s", got)
	}
	if slices.Contains(a.Agent.Config().Extensions.Disabled, "probe") {
		t.Errorf("extensions.disabled = %v", a.Agent.Config().Extensions.Disabled)
	}
	if file := readFile(t, a.Paths.GlobalFile()); strings.Contains(file, `"probe"`) {
		t.Errorf("config file still disables probe:\n%s", file)
	}
}

func TestSetExtensionEnabledErrors(t *testing.T) {
	t.Run("extensions off", func(t *testing.T) {
		a := testApp(t, "", "", false)
		if _, err := a.SetExtensionEnabled(context.Background(), "probe", true); err == nil || !strings.Contains(err.Error(), "turned off") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("unknown extension", func(t *testing.T) {
		a := extApp(t, "")
		if _, err := a.SetExtensionEnabled(context.Background(), "nope", false); err == nil || !strings.Contains(err.Error(), `no extension named "nope"`) {
			t.Errorf("err = %v", err)
		}
		if file := readFile(t, a.Paths.GlobalFile()); file != "" {
			t.Errorf("config written for an unknown extension:\n%s", file)
		}
	})
	t.Run("busy", func(t *testing.T) {
		a := extApp(t, "", fake.Step{Wait: true})
		whileBusy(t, a, func() {
			if _, err := a.SetExtensionEnabled(context.Background(), "probe", false); !errors.Is(err, agent.ErrBusy) {
				t.Errorf("err = %v, want ErrBusy", err)
			}
		})
		if got := extStatus(t, a, "probe"); got != ext.StatusRunning {
			t.Errorf("status = %s", got)
		}
	})
	t.Run("project keeps it disabled", func(t *testing.T) {
		a := extApp(t, `{"extensions": {"disabled": ["probe"]}}`)
		if got := extStatus(t, a, "probe"); got != ext.StatusDisabled {
			t.Fatalf("status = %s", got)
		}
		note, err := a.SetExtensionEnabled(context.Background(), "probe", true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(note, "also disables probe") {
			t.Errorf("note = %q", note)
		}
		if !slices.Contains(toolNames(a), "session_probe") {
			t.Errorf("tools = %v", toolNames(a))
		}
	})
}

func TestReloadExtensions(t *testing.T) {
	t.Run("extensions off", func(t *testing.T) {
		a := testApp(t, "", "", false)
		if err := a.ReloadExtensions(context.Background()); err != nil {
			t.Error(err)
		}
	})
	t.Run("picks up a new extension and drops a removed one", func(t *testing.T) {
		a := extApp(t, "")
		writeFile(t, filepath.Join(a.Paths.Home, "extensions", "search", "search.go"), searchExtension)
		if err := os.RemoveAll(filepath.Join(a.Paths.Home, "extensions", "probe")); err != nil {
			t.Fatal(err)
		}
		if names := toolNames(a); slices.Contains(names, "word_count") || !slices.Contains(names, "session_probe") {
			t.Fatalf("tools changed before the reload: %v", names)
		}
		if err := a.ReloadExtensions(context.Background()); err != nil {
			t.Fatal(err)
		}
		names := toolNames(a)
		if !slices.Contains(names, "word_count") || slices.Contains(names, "session_probe") {
			t.Errorf("tools after reload = %v", names)
		}
		if slices.ContainsFunc(a.Ext.Extensions(), func(e *ext.Extension) bool { return e.ID == "probe" }) {
			t.Error("the removed extension is still listed")
		}
		if got := extStatus(t, a, "search"); got != ext.StatusRunning {
			t.Errorf("search status = %s", got)
		}
	})
}
