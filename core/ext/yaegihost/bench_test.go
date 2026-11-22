package yaegihost

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"

	"github.com/NDari/rho/core/ext/internal/benchwork"
)

// The benchmarks run the same workloads (core/ext/internal/benchwork)
// compiled and interpreted by Yaegi, as an extension would run them:
//
//	go test -run '^$' -bench Workloads ./core/ext/yaegihost/
//
// Compare the native and yaegi times of each workload for the slowdown.

func workSource(tb testing.TB) string {
	tb.Helper()
	src, err := os.ReadFile(filepath.Join("..", "internal", "benchwork", "work.go"))
	if err != nil {
		tb.Fatal(err)
	}
	return benchwork.Source(src)
}

// interpreted loads the workloads into a fresh interpreter set up like the
// extension host's, and returns its functions by name.
func interpreted(tb testing.TB) func(name string) reflect.Value {
	tb.Helper()
	i := interp.New(interp.Options{})
	if err := i.Use(stdlib.Symbols); err != nil {
		tb.Fatal(err)
	}
	if err := i.Use(Symbols); err != nil {
		tb.Fatal(err)
	}
	if _, err := i.Eval(workSource(tb)); err != nil {
		tb.Fatal(err)
	}
	return func(name string) reflect.Value {
		v, err := i.Eval(name)
		if err != nil {
			tb.Fatal(err)
		}
		return v
	}
}

type workload struct {
	name   string
	bytes  int
	native func()
	yaegi  func(get func(string) reflect.Value) func()
}

func workloads(tb testing.TB) []workload {
	reply := benchwork.Reply()
	small := benchwork.Transcript(1000)
	large := benchwork.Transcript(100_000)
	smallMaps, largeMaps := benchwork.Maps(small), benchwork.Maps(large)
	var sink any
	maps := func(fn, size string, data []byte, msgs []map[string]any, native func([]map[string]any) any) workload {
		return workload{
			name: fn + "/" + size, bytes: len(data),
			native: func() { sink = native(msgs) },
			yaegi: func(get func(string) reflect.Value) func() {
				f := get(fn)
				arg := []reflect.Value{reflect.ValueOf(msgs)}
				return func() { sink = f.Call(arg)[0].Interface() }
			},
		}
	}
	_ = sink
	return []workload{
		{
			name: "ScanStream/4B-chunks", bytes: len(reply),
			native: func() { sink = benchwork.ScanStream(reply, 4) },
			yaegi: func(get func(string) reflect.Value) func() {
				f := get("ScanStream").Interface().(func(string, int) int)
				return func() { sink = f(reply, 4) }
			},
		},
		maps("EstimateTokens", "1k", small, smallMaps, func(m []map[string]any) any { return benchwork.EstimateTokens(m) }),
		maps("EstimateTokens", "100k", large, largeMaps, func(m []map[string]any) any { return benchwork.EstimateTokens(m) }),
		maps("EstimateTokensRunes", "1k", small, smallMaps, func(m []map[string]any) any { return benchwork.EstimateTokensRunes(m) }),
		maps("EstimateTokensRunes", "100k", large, largeMaps, func(m []map[string]any) any { return benchwork.EstimateTokensRunes(m) }),
		maps("Redact", "1k", small, smallMaps, func(m []map[string]any) any { return benchwork.Redact(m) }),
		maps("Redact", "100k", large, largeMaps, func(m []map[string]any) any { return benchwork.Redact(m) }),
		maps("Rewrite", "1k", small, smallMaps, func(m []map[string]any) any { return benchwork.Rewrite(m) }),
		maps("Rewrite", "100k", large, largeMaps, func(m []map[string]any) any { return benchwork.Rewrite(m) }),
		{
			name: "RoundTrip/100k", bytes: len(large),
			native: func() { sink = benchwork.RoundTrip(large) },
			yaegi: func(get func(string) reflect.Value) func() {
				f := get("RoundTrip").Interface().(func([]byte) int)
				return func() { sink = f(large) }
			},
		},
	}
}

func BenchmarkWorkloads(b *testing.B) {
	get := interpreted(b)
	for _, w := range workloads(b) {
		run := w.yaegi(get)
		b.Run(w.name+"/native", func(b *testing.B) {
			b.SetBytes(int64(w.bytes))
			for b.Loop() {
				w.native()
			}
		})
		b.Run(w.name+"/yaegi", func(b *testing.B) {
			b.SetBytes(int64(w.bytes))
			for b.Loop() {
				run()
			}
		})
	}
}

// BenchmarkLoad is the time to set up an interpreter and load the
// workloads (about 200 lines), part of an extension's start.
func BenchmarkLoad(b *testing.B) {
	for b.Loop() {
		interpreted(b)
	}
}

// TestWorkloadsAgree keeps the two runs comparable: both must compute the
// same results.
func TestWorkloadsAgree(t *testing.T) {
	get := interpreted(t)
	reply := benchwork.Reply()
	msgs := benchwork.Maps(benchwork.Transcript(2000))
	if n, y := benchwork.ScanStream(reply, 4), get("ScanStream").Interface().(func(string, int) int)(reply, 4); n != y || n == 0 {
		t.Errorf("ScanStream: native %d, yaegi %d", n, y)
	}
	call := func(name string) any {
		return get(name).Call([]reflect.Value{reflect.ValueOf(msgs)})[0].Interface()
	}
	if n, y := benchwork.EstimateTokens(msgs), call("EstimateTokens"); n != y || n == 0 {
		t.Errorf("EstimateTokens: native %d, yaegi %v", n, y)
	}
	if n, y := benchwork.EstimateTokensRunes(msgs), call("EstimateTokensRunes"); n != y || n != benchwork.EstimateTokens(msgs) {
		t.Errorf("EstimateTokensRunes: native %d, yaegi %v", n, y)
	}
	if n, y := benchwork.Redact(msgs), call("Redact"); n != y || n == 0 {
		t.Errorf("Redact: native %d, yaegi %v", n, y)
	}
	if n, y := len(benchwork.Rewrite(msgs)), reflect.ValueOf(call("Rewrite")).Len(); n != y {
		t.Errorf("Rewrite: native %d, yaegi %d", n, y)
	}
}
