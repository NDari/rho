package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// maxStdin is the most prompt text read from stdin.
const maxStdin = 10 << 20

// stdinWait is how long to wait for piped input when a prompt was also
// given as an argument. Some launchers leave stdin open as a pipe that
// never receives data or closes.
var stdinWait = 3 * time.Second

// readStdin returns prompt text piped or redirected into f. It ignores
// terminals, sockets and devices, which may never reach end of file. With
// haveArg set, it stops waiting for a pipe that stays silent.
func readStdin(f *os.File, haveArg bool, warn io.Writer) (string, error) {
	st, err := f.Stat()
	if err != nil {
		return "", nil //nolint:nilerr // no usable stdin (e.g. a detached process) means no piped input
	}
	mode := st.Mode()
	switch {
	case mode.IsRegular():
		data, err := io.ReadAll(io.LimitReader(f, maxStdin))
		return string(data), err
	case mode&os.ModeNamedPipe == 0:
		return "", nil
	}
	if !haveArg {
		data, err := io.ReadAll(io.LimitReader(f, maxStdin))
		return string(data), err
	}
	type chunk struct {
		data []byte
		err  error
	}
	first := make(chan chunk, 1)
	go func() {
		buf := make([]byte, 32<<10)
		n, err := f.Read(buf)
		first <- chunk{buf[:n], err}
	}()
	select {
	case c := <-first:
		if errors.Is(c.err, io.EOF) {
			return string(c.data), nil
		}
		if c.err != nil {
			return "", c.err
		}
		rest, err := io.ReadAll(io.LimitReader(f, maxStdin-int64(len(c.data))))
		return string(c.data) + string(rest), err
	case <-time.After(stdinWait):
		fmt.Fprintln(warn, "rho: no input on stdin; continuing with the prompt argument only")
		return "", nil
	}
}
