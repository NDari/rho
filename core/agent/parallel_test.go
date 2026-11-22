package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
	"github.com/NDari/rho/core/tools"
)

// meetTool succeeds only if every call to it is running at the same time:
// each waits until all of them have started.
type meetTool struct {
	want    int
	mu      sync.Mutex
	started int
	all     chan struct{}
}

func (*meetTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{Name: "meet", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (*meetTool) ReadOnly() bool   { return true }
func (*meetTool) Concurrent() bool { return true }
func (m *meetTool) Execute(context.Context, *tools.Env, json.RawMessage, func(string)) tools.Result {
	m.mu.Lock()
	m.started++
	if m.started == m.want {
		close(m.all)
	}
	m.mu.Unlock()
	select {
	case <-m.all:
		return tools.Result{Content: []provider.Block{provider.Text("met")}}
	case <-time.After(5 * time.Second):
		return tools.Result{Content: []provider.Block{provider.Text("ran alone")}, IsError: true}
	}
}

func TestConcurrentToolsRunInParallel(t *testing.T) {
	meet := &meetTool{want: 3, all: make(chan struct{})}
	fp := fake.New("fake",
		fake.Step{Calls: []fake.Call{{ID: "a", Name: "meet"}, {ID: "b", Name: "meet"}, {ID: "c", Name: "meet"}}},
		fake.Step{Text: "done"},
	)
	a, _, _ := newTestAgent(t, fp)
	a.SetTools(append(tools.Builtins(), meet))
	if err := a.Run(context.Background(), provider.UserText("go")); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range a.Session().BuildContext().Messages {
		if m.Role == provider.RoleToolResult {
			b := m.Content[0]
			if b.IsError {
				t.Errorf("call %s did not run in parallel: %s", b.CallID, b.Content[0].Text)
			}
			ids = append(ids, b.CallID)
		}
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Errorf("results saved as %v, want call order", ids)
	}
}

func TestWritesSplitParallelBatches(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Calls: []fake.Call{
			{ID: "r1", Name: "read", Args: map[string]string{"path": "f.txt"}},
			{ID: "w", Name: "write", Args: map[string]string{"path": "f.txt", "content": "new\n"}},
			{ID: "r2", Name: "read", Args: map[string]string{"path": "f.txt"}},
		}},
		fake.Step{Text: "done"},
	)
	a, _, dir := newTestAgent(t, fp)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), provider.UserText("go")); err != nil {
		t.Fatal(err)
	}
	results := map[string]string{}
	var order []string
	for _, m := range a.Session().BuildContext().Messages {
		if m.Role == provider.RoleToolResult {
			b := m.Content[0]
			results[b.CallID] = b.Content[0].Text
			order = append(order, b.CallID)
		}
	}
	// The write runs alone between the reads: r1 sees the old content and
	// r2 the new one.
	if strings.Join(order, ",") != "r1,w,r2" || !strings.Contains(results["r1"], "old") || !strings.Contains(results["r2"], "new") {
		t.Errorf("order %v, results %q", order, results)
	}
}
