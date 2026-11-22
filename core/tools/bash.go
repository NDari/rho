package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
)

// Bash output limits for the model.
const (
	BashMaxLines = 2000
	BashMaxBytes = 50 * 1024
	// bashTailKeep is how much recent output is kept in memory.
	bashTailKeep = 256 * 1024
)

// Bash runs shell commands.
type Bash struct{}

// Spec implements Tool.
func (Bash) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "bash",
		Description: "Run a bash command in the working directory and return its combined stdout and stderr. " +
			fmt.Sprintf("Long output is truncated to the last %d lines or %d KB; the full output is saved to a file whose path is included. ", BashMaxLines, BashMaxBytes/1024) +
			"Commands are not interactive (no stdin). Use timeout (seconds) for long-running commands.",
		Parameters: schema(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "The bash command to run"},
    "timeout": {"type": "integer", "description": "Timeout in seconds"}
  },
  "required": ["command"]
}`),
	}
}

// ReadOnly implements Tool. No shell command can be guaranteed read-only.
func (Bash) ReadOnly() bool { return false }

type bashArgs struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// Execute implements Tool.
func (Bash) Execute(ctx context.Context, env *Env, raw json.RawMessage, progress func(string)) Result {
	var a bashArgs
	if err := decodeArgs(raw, &a); err != nil {
		return errorResult("%v", err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return errorResult("command is required")
	}
	timeout := env.BashTimeout
	if timeout <= 0 {
		timeout = 120
	}
	if a.Timeout > 0 {
		timeout = a.Timeout
	}
	if env.BashMaxTimeout > 0 && timeout > env.BashMaxTimeout {
		timeout = env.BashMaxTimeout
	}
	shell, err := findShell(env.Shell)
	if err != nil {
		return errorResult("%v", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	out, err := newOutput(progress)
	if err != nil {
		return errorResult("cannot create output file: %v", err)
	}
	defer out.close()

	cmd := exec.Command(shell, "-c", a.Command)
	cmd.Dir = env.Cwd
	cmd.Env = append(os.Environ(), "PAGER=cat", "GIT_PAGER=cat")
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = 2 * time.Second // don't hang on pipes held by background children
	setupProcess(cmd)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return errorResult("cannot start shell %s: %v", shell, err)
	}
	tree := trackProcess(cmd)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-runCtx.Done():
		tree.kill()
		select {
		case waitErr = <-done:
		case <-time.After(5 * time.Second):
			waitErr = errors.New("process did not exit after kill")
		}
	}
	tree.release()
	elapsed := time.Since(start)

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	text, truncated, fullPath := out.result()

	var status string
	isError := false
	switch {
	case ctx.Err() != nil:
		status = "Command cancelled by " + events.CancelledBy(ctx) + "."
		isError = true
	case runCtx.Err() != nil:
		status = fmt.Sprintf("Command timed out after %ds and was killed.", timeout)
		isError = true
	case exitCode != 0:
		status = fmt.Sprintf("Exit code: %d", exitCode)
		isError = true
		if exitCode == -1 && waitErr != nil {
			status = fmt.Sprintf("Command failed: %v", waitErr)
		}
	default:
		status = "Exit code: 0"
	}

	var b strings.Builder
	if truncated {
		fmt.Fprintf(&b, "[Output truncated: showing the last %d lines of %d (%s total). Full output: %s]\n",
			countLines(text), out.lines, humanSize(out.total), fullPath)
	}
	if text == "" {
		b.WriteString("(no output)\n")
	} else {
		b.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString(status)

	code := exitCode
	return Result{
		Content: []provider.Block{provider.Text(b.String())},
		IsError: isError,
		Display: &events.Display{
			Kind:           "text",
			Text:           text,
			ExitCode:       &code,
			Truncated:      truncated,
			FullOutputPath: fullPath,
			Summary:        fmt.Sprintf("%s · %s", status, elapsed.Round(10*time.Millisecond)),
		},
	}
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// StripANSI removes terminal escape sequences.
func StripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// output collects command output: everything goes to a temp file, and a
// bounded tail is kept in memory.
type output struct {
	mu       sync.Mutex
	file     *os.File
	tail     []byte
	total    int64
	lines    int
	keep     bool
	progress func(string)
}

func newOutput(progress func(string)) (*output, error) {
	f, err := os.CreateTemp("", "rho-bash-*.log")
	if err != nil {
		return nil, err
	}
	return &output{file: f, progress: progress}, nil
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _ = o.file.Write(p)
	o.total += int64(len(p))
	o.lines += strings.Count(string(p), "\n")
	o.tail = append(o.tail, p...)
	if len(o.tail) > 2*bashTailKeep {
		o.tail = append([]byte(nil), o.tail[len(o.tail)-bashTailKeep:]...)
	}
	if o.progress != nil {
		o.progress(StripANSI(string(p)))
	}
	return len(p), nil
}

// result returns the model-facing text, whether it was truncated, and the
// path of the full output (empty when not truncated).
func (o *output) result() (string, bool, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	text := StripANSI(strings.ReplaceAll(string(o.tail), "\r\n", "\n"))
	truncated := int64(len(o.tail)) < o.total
	lines := strings.Split(text, "\n")
	if len(lines) > BashMaxLines+1 {
		lines = lines[len(lines)-BashMaxLines-1:]
		truncated = true
	}
	text = strings.Join(lines, "\n")
	if len(text) > BashMaxBytes {
		text = text[len(text)-BashMaxBytes:]
		if i := strings.IndexByte(text, '\n'); i >= 0 && i < len(text)-1 {
			text = text[i+1:]
		}
		truncated = true
	}
	if !truncated {
		return text, false, ""
	}
	o.keep = true
	return text, true, o.file.Name()
}

func (o *output) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	name := o.file.Name()
	_ = o.file.Close()
	if !o.keep {
		_ = os.Remove(name)
	}
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
