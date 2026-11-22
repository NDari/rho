package ext_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/ext/internal/benchwork"
	"github.com/NDari/rho/core/provider"
)

// These benchmarks measure what a hook costs end to end, through the
// manager, JSON-RPC and the extension, for one extension launched each way:
//
//	go test -run '^$' -bench Hook ./core/ext/
//
//   - none: no extension, Rho's own cost.
//   - yaegi-inprocess: the Yaegi host in this process over pipes (as the
//     other tests run extensions), so no process scheduling.
//   - yaegi-process: "rho ext-host", as Rho runs Go extensions.
//   - compiled-process: a compiled executable extension with the same hooks
//     (testdata/benchext), as Rho runs executable extensions.
//
// The process modes build rho and benchext first (go build, cached).

const benchExtNoop = `package main

import "rho"

func main() {
	rho.On("user_message", func(e rho.Event) rho.Response { return rho.Response{} })
	rho.On("before_request", func(e rho.Event) rho.Response { return rho.Response{} })
}
`

// benchExtRewrite also needs benchwork's work.go next to it, for Rewrite.
const benchExtRewrite = `package main

import "rho"

func main() {
	rho.On("user_message", func(e rho.Event) rho.Response { return rho.Response{} })
	rho.On("before_request", func(e rho.Event) rho.Response { return rho.Response{Messages: Rewrite(e.Messages)} })
}
`

var (
	buildOnce          sync.Once
	rhoBin, benchExtEx string
	buildErr           error
)

// buildBinaries builds rho (for ext-host) and the compiled extension once
// per test binary.
func buildBinaries(b *testing.B) (string, string) {
	b.Helper()
	buildOnce.Do(func() {
		// One fixed directory, rebuilt each run, so runs leave nothing behind.
		dir := filepath.Join(os.TempDir(), "rho-ext-bench")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			buildErr = err
			return
		}
		rhoBin, benchExtEx = filepath.Join(dir, "rho"), filepath.Join(dir, "benchext")
		for out, pkg := range map[string]string{rhoBin: "../../cmd/rho", benchExtEx: "./testdata/benchext"} {
			if o, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("go build %s: %v\n%s", pkg, err, o)
				return
			}
		}
	})
	if buildErr != nil {
		b.Skip(buildErr)
	}
	return rhoBin, benchExtEx
}

// benchManager starts a manager with one extension launched as mode, doing
// work ("noop" or "rewrite") in before_request.
func benchManager(b *testing.B, mode, work string) *ext.Manager {
	b.Helper()
	home := b.TempDir()
	dir := filepath.Join(home, "extensions", "bench")
	o := ext.Options{
		Paths:  config.Paths{Home: home, Cwd: home, ProjectRoot: home, ProjectDir: filepath.Join(b.TempDir(), ".rho")},
		Config: config.Defaults(),
		UI:     &fakeUI{},
	}
	write := func(name, body string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	goExtension := func() {
		if work == "rewrite" {
			src, err := os.ReadFile(filepath.Join("internal", "benchwork", "work.go"))
			if err != nil {
				b.Fatal(err)
			}
			write("ext.go", benchExtRewrite)
			write("work.go", benchwork.Source(src))
			return
		}
		write("ext.go", benchExtNoop)
	}
	switch mode {
	case "none":
	case "yaegi-inprocess":
		goExtension()
		o.Launch = inProcess
	case "yaegi-process":
		rho, _ := buildBinaries(b)
		goExtension()
		o.ExtHost = rho
	case "compiled-process":
		_, exe := buildBinaries(b)
		write("rho-extension.toml", fmt.Sprintf("name = \"bench\"\ncommand = %q\nargs = [%q]\n", exe, work))
	}
	m := ext.NewManager(o)
	m.Start(context.Background())
	b.Cleanup(m.Stop)
	if mode != "none" {
		if exts := m.Extensions(); len(exts) != 1 || exts[0].Status != ext.StatusRunning {
			b.Fatalf("extension not running: %s", m.Describe())
		}
	}
	return m
}

var benchModes = []string{"none", "yaegi-inprocess", "yaegi-process", "compiled-process"}

// BenchmarkHookRoundTrip is one small hook call: the fixed cost a
// middleware would pay per streamed event.
func BenchmarkHookRoundTrip(b *testing.B) {
	for _, mode := range benchModes {
		b.Run(mode, func(b *testing.B) {
			m := benchManager(b, mode, "noop")
			ctx := context.Background()
			for b.Loop() {
				if text, _ := m.UserMessage(ctx, "hi"); text != "hi" {
					b.Fatalf("text = %q", text)
				}
			}
		})
	}
}

// BenchmarkHookBeforeRequest sends a whole conversation to before_request,
// which does nothing (noop) or returns it rewritten (rewrite).
func BenchmarkHookBeforeRequest(b *testing.B) {
	for _, size := range []int{1000, 10_000, 100_000} {
		var msgs []provider.Message
		data := benchwork.Transcript(size)
		if err := json.Unmarshal(data, &msgs); err != nil {
			b.Fatal(err)
		}
		for _, work := range []string{"noop", "rewrite"} {
			for _, mode := range benchModes {
				if mode == "none" && work == "rewrite" {
					continue
				}
				b.Run(fmt.Sprintf("%dk/%s/%s", size/1000, work, mode), func(b *testing.B) {
					m := benchManager(b, mode, work)
					ctx := context.Background()
					b.SetBytes(int64(len(data)))
					for b.Loop() {
						_, got, _ := m.BeforeRequest(ctx, "system", msgs, []string{"read"}, false)
						if len(got) < len(msgs) {
							b.Fatalf("got %d messages, want %d", len(got), len(msgs))
						}
					}
				})
			}
		}
	}
}
