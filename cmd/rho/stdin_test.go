package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadStdinRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "in.txt")
	if err := os.WriteFile(path, []byte("from file"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := readStdin(f, true, &bytes.Buffer{})
	if err != nil || got != "from file" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReadStdinDeviceIgnored(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := readStdin(f, false, &bytes.Buffer{})
	if err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReadStdinPipe(t *testing.T) {
	for _, haveArg := range []bool{false, true} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_, _ = w.WriteString("piped ")
			time.Sleep(20 * time.Millisecond)
			_, _ = w.WriteString("text")
			w.Close()
		}()
		got, err := readStdin(r, haveArg, &bytes.Buffer{})
		r.Close()
		if err != nil || got != "piped text" {
			t.Fatalf("haveArg=%v: got %q, %v", haveArg, got, err)
		}
	}
}

func TestReadStdinSilentPipeDoesNotHang(t *testing.T) {
	old := stdinWait
	stdinWait = 50 * time.Millisecond
	defer func() { stdinWait = old }()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	defer r.Close()
	var warn bytes.Buffer
	got, err := readStdin(r, true, &warn)
	if err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
	if !strings.Contains(warn.String(), "no input on stdin") {
		t.Errorf("warning = %q", warn.String())
	}
}
