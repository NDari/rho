package ext_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/tools"
)

// buildCrashExt compiles testdata/crashext into a temp dir.
func buildCrashExt(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "crashext.exe")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/crashext").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

type noticeLog struct {
	mu   sync.Mutex
	list []string
}

func (l *noticeLog) sink(e events.Event) {
	if n, ok := e.(events.Notice); ok {
		l.mu.Lock()
		l.list = append(l.list, n.Level+": "+n.Text)
		l.mu.Unlock()
	}
}

func (l *noticeLog) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.list, "\n")
}

func (l *noticeLog) waitFor(t *testing.T, sub string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(l.all(), sub) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no notice containing %q; got:\n%s", sub, l.all())
}

func toolNamed(m *ext.Manager, name string) tools.Tool {
	for _, t := range m.Tools() {
		if t.Spec().Name == name {
			return t
		}
	}
	return nil
}

// TestCrashedExtensionRestarts runs a real extension process that dies
// mid-call: the call fails with the extension's last words, the process is
// restarted once with its tools offered again, and a second crash soon
// after is left for /reload so a crash loop stops.
func TestCrashedExtensionRestarts(t *testing.T) {
	bin := buildCrashExt(t)
	home := t.TempDir()
	dir := filepath.Join(home, "extensions", "crashy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ext.ManifestName), fmt.Appendf(nil, "command = %q\n", bin), 0o644); err != nil {
		t.Fatal(err)
	}
	var notices noticeLog
	var restarts atomic.Int32
	m := ext.NewManager(ext.Options{
		Paths:     config.Paths{Home: home, Cwd: home, ProjectDir: filepath.Join(home, "none")},
		Config:    config.Defaults(),
		Sink:      notices.sink,
		OnRestart: func() { restarts.Add(1) },
	})
	m.SetRestartDelay(time.Millisecond)
	ctx := context.Background()
	m.Start(ctx)
	defer m.Stop()

	work := func() string {
		t.Helper()
		res := toolNamed(m, "work").Execute(ctx, &tools.Env{}, json.RawMessage(`{}`), nil)
		if res.IsError {
			t.Fatalf("work failed: %+v", res)
		}
		return res.Content[0].Text
	}
	pid1 := work()
	oldWork := toolNamed(m, "work")

	res := toolNamed(m, "crash").Execute(ctx, &tools.Env{}, json.RawMessage(`{}`), nil)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "extension error") {
		t.Fatalf("crash result = %+v", res)
	}
	// A call through the crashed process's tool waits for the restart
	// (as when the model answers before the tools are refreshed).
	if res := oldWork.Execute(ctx, &tools.Env{}, json.RawMessage(`{}`), nil); res.IsError || res.Content[0].Text == pid1 {
		t.Errorf("call during the restart = %+v", res)
	}
	notices.waitFor(t, "extension crashy crashed")
	if !strings.Contains(notices.all(), "index out of range") {
		t.Errorf("crash notice lacks the extension's stderr:\n%s", notices.all())
	}
	notices.waitFor(t, "extension crashy restarted")
	if restarts.Load() != 1 {
		t.Errorf("OnRestart called %d times", restarts.Load())
	}
	// The manager offers the new process's tools.
	if pid2 := work(); pid2 == pid1 {
		t.Errorf("work answered from the crashed process %s", pid1)
	}

	// A second crash within the window is not restarted.
	_ = toolNamed(m, "crash").Execute(ctx, &tools.Env{}, json.RawMessage(`{}`), nil)
	notices.waitFor(t, "/reload to restart")
	time.Sleep(50 * time.Millisecond)
	if restarts.Load() != 1 {
		t.Errorf("restarted again within the window (%d)", restarts.Load())
	}
	if exts := m.Extensions(); exts[0].Status != ext.StatusFailed {
		t.Errorf("status = %s", exts[0].Status)
	}

	// /reload brings it back.
	if err := m.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	work()
}

// TestStoppedExtensionIsNotRestarted checks that a deliberate stop is not
// mistaken for a crash.
func TestStoppedExtensionIsNotRestarted(t *testing.T) {
	bin := buildCrashExt(t)
	home := t.TempDir()
	dir := filepath.Join(home, "extensions", "crashy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ext.ManifestName), fmt.Appendf(nil, "command = %q\n", bin), 0o644); err != nil {
		t.Fatal(err)
	}
	var notices noticeLog
	var restarts atomic.Int32
	m := ext.NewManager(ext.Options{
		Paths:     config.Paths{Home: home, Cwd: home, ProjectDir: filepath.Join(home, "none")},
		Config:    config.Defaults(),
		Sink:      notices.sink,
		OnRestart: func() { restarts.Add(1) },
	})
	m.SetRestartDelay(time.Millisecond)
	ctx := context.Background()
	m.Start(ctx)
	if err := m.SetEnabled(ctx, "crashy", false); err != nil {
		t.Fatal(err)
	}
	m.Stop()
	time.Sleep(100 * time.Millisecond)
	if restarts.Load() != 0 || strings.Contains(notices.all(), "crashed") {
		t.Errorf("a stopped extension was treated as crashed:\n%s", notices.all())
	}
}
