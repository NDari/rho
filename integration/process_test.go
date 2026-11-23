package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/provider/replay"
)

var (
	buildOnce sync.Once
	binDir    string
	buildErr  error
)

// binaries builds rho and the crashing test extension once per run.
func binaries(t *testing.T) (rho, crashExt string) {
	t.Helper()
	buildOnce.Do(func() {
		binDir, buildErr = os.MkdirTemp("", "rho-integration-")
		if buildErr != nil {
			return
		}
		for out, pkg := range map[string]string{"rho": "../cmd/rho", "crashext": "../core/ext/testdata/crashext"} {
			if o, err := exec.Command("go", "build", "-o", filepath.Join(binDir, out), pkg).CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("go build %s: %w\n%s", pkg, err, o)
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return filepath.Join(binDir, "rho"), filepath.Join(binDir, "crashext")
}

func TestMain(m *testing.M) {
	code := m.Run()
	if binDir != "" {
		_ = os.RemoveAll(binDir)
	}
	os.Exit(code)
}

// proc is a running `rho -p --json`.
type proc struct {
	cmd    *exec.Cmd
	lines  chan map[string]any
	stderr bytes.Buffer
	done   chan struct{}
}

func (e *env) start(t *testing.T, args ...string) *proc {
	t.Helper()
	rho, _ := binaries(t)
	cmd := exec.Command(rho, append([]string{"-p", "--json"}, args...)...)
	cmd.Dir = e.cwd
	cmd.Env = append(os.Environ(), "RHO_HOME="+e.home, "RHO_NO_UPDATE_CHECK=1")
	p := &proc{cmd: cmd, lines: make(chan map[string]any, 1024), done: make(chan struct{})}
	cmd.Stderr = &p.stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(p.lines)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				p.lines <- m
			}
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-p.done
	})
	go func() { _ = cmd.Wait(); close(p.done) }()
	return p
}

// waitFor reads events until one satisfies f.
func (p *proc) waitFor(t *testing.T, what string, f func(map[string]any) bool) {
	t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case l, ok := <-p.lines:
			if !ok {
				t.Fatalf("rho exited before %s; stderr:\n%s", what, p.stderr.String())
			}
			if f(l) {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// finish waits for exit and returns the remaining events and exit code.
func (p *proc) finish(t *testing.T) ([]map[string]any, int) {
	t.Helper()
	var rest []map[string]any
	timeout := time.After(20 * time.Second)
	for {
		select {
		case l, ok := <-p.lines:
			if !ok {
				select {
				case <-p.done:
				case <-timeout:
					t.Fatal("rho did not exit")
				}
				return rest, p.cmd.ProcessState.ExitCode()
			}
			rest = append(rest, l)
		case <-timeout:
			t.Fatal("rho did not exit")
		}
	}
}

func typed(typ string) func(map[string]any) bool {
	return func(l map[string]any) bool { return l["type"] == typ }
}

// TestInterruptMidStream sends Ctrl-C (SIGINT) to rho while the model
// streams: rho stops, says so, exits non-zero, and keeps what streamed.
func TestInterruptMidStream(t *testing.T) {
	body := replay.Recording("llamacpp_chat_text.sse")
	e := newEnv(t, replay.Reply{Body: body, CutAt: len(body) / 2, Stall: true})
	p := e.start(t, "What is 17*23?")
	p.waitFor(t, "streamed thinking", typed("thinking_delta"))
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	rest, code := p.finish(t)
	if code == 0 {
		t.Error("exit code 0 after an interrupt")
	}
	res := rest[len(rest)-1]
	if res["type"] != "result" || res["status"] != "cancelled" {
		t.Fatalf("last line = %v", res)
	}
	s := reopen(t, res["session"].(string))
	if got := roles(s); got != "user assistant[cancelled]" {
		t.Errorf("session = %s", got)
	}
	if cv := s.BuildContext(); !strings.Contains(cv.Messages[1].Content[0].Text, "17") {
		t.Errorf("partial thinking not kept: %+v", cv.Messages[1].Content)
	}
}

// TestKilledMidTurnThenContinue kills rho (SIGKILL, no cleanup) after a
// tool ran and while the next reply streams, then leaves the session file
// as an interrupted write would: an incomplete last line. `rho -c` must
// warn, continue the conversation from what was saved, and leave a file
// that opens cleanly.
func TestKilledMidTurnThenContinue(t *testing.T) {
	stalled := replay.Reply{Body: recordedText.Body, CutAt: half(recordedText), Stall: true}
	e := newEnv(t, recordedToolCall, stalled)
	p := e.start(t, "What is the module name?")
	p.waitFor(t, "the read tool to finish", typed("tool_end"))
	p.waitFor(t, "the second reply to stream", typed("thinking_delta"))
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-p.done

	sessions, _ := filepath.Glob(filepath.Join(e.home, "sessions", "*", "*.jsonl"))
	if len(sessions) != 1 {
		t.Fatalf("sessions = %v", sessions)
	}
	path := sessions[0]
	if got := roles(reopen(t, path)); got != "user assistant(read) tool_result" {
		t.Fatalf("saved before the kill: %s", got)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"id":"5f2c9a1e","parent":"x","type":"message","ts":"2026-11-22T10:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"The mod`)
	_ = f.Close()

	e.srv.Add(textReply("The module is example.com/demo."))
	p2 := e.start(t, "-c", "So what is it?")
	rest, code := p2.finish(t)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, p2.stderr.String())
	}
	if !strings.Contains(p2.stderr.String(), "is damaged") || !strings.Contains(p2.stderr.String(), "incomplete final entry") {
		t.Errorf("no damage warning on stderr:\n%s", p2.stderr.String())
	}
	res := rest[len(rest)-1]
	if res["status"] != "done" || res["session"] != path {
		t.Fatalf("result = %v", res)
	}
	if got := roles(reopen(t, path)); got != "user assistant(read) tool_result user assistant" {
		t.Errorf("session after continuing = %s", got)
	}
	// The server got the whole saved conversation, tool result included.
	msgs := e.sentMessages(len(e.srv.Requests()) - 1)
	var sent []string
	for _, m := range msgs {
		sent = append(sent, m.(map[string]any)["role"].(string))
	}
	if got := strings.Join(sent, " "); got != "system user assistant tool user" {
		t.Errorf("sent roles = %s", got)
	}
}

// TestExtensionCrashMidTurn runs a real extension process that crashes
// during a tool call in a turn: the model sees the error, the extension
// is restarted, and the model's next call to it works, all in one turn.
func TestExtensionCrashMidTurn(t *testing.T) {
	_, crashExt := binaries(t)
	e := newEnv(t,
		callReply("crash", `{}`),
		callReply("work", `{}`),
		textReply("The extension recovered."),
	)
	e.write(filepath.Join(e.home, "extensions", "crashy", ext.ManifestName), fmt.Sprintf("command = %q\n", crashExt))
	r := e.run("Try the extension.", runOpts{extensions: true})
	if r.err != nil || r.result["status"] != "done" {
		t.Fatalf("err %v, result %v\n%s", r.err, r.result, r.stderr)
	}
	ends := r.ofType("tool_end")
	if len(ends) != 2 {
		t.Fatalf("tool_end = %v", ends)
	}
	if !toolFailed(ends[0]) || toolFailed(ends[1]) {
		t.Errorf("crash call failed %v, work call failed %v: %v", toolFailed(ends[0]), toolFailed(ends[1]), ends)
	}
	errs := r.notices("error")
	if !strings.Contains(errs, "extension crashy crashed") || !strings.Contains(errs, "restarting it") {
		t.Errorf("error notices:\n%s", errs)
	}
	// The model was told what happened.
	msgs := e.sentMessages(1)
	last, _ := json.Marshal(msgs[len(msgs)-1])
	if !strings.Contains(string(last), "extension error") {
		t.Errorf("tool result sent = %s", last)
	}
	if got := roles(reopen(t, r.path)); got != "user assistant(crash) tool_result assistant(work) tool_result assistant" {
		t.Errorf("session = %s", got)
	}
}
