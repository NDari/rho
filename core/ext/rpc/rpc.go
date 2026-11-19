// Package rpc implements newline-delimited JSON-RPC 2.0 over a pair of
// streams, usable from both ends (Rho and an extension).
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Message is a JSON-RPC 2.0 request, notification or response.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Standard error codes.
const (
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// Handler handles incoming requests and notifications. For notifications
// the result is ignored.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// ErrClosed is returned when the connection has closed.
var ErrClosed = errors.New("connection closed")

// Conn is a bidirectional JSON-RPC connection.
type Conn struct {
	w       io.Writer
	wmu     sync.Mutex
	nextID  atomic.Int64
	pmu     sync.Mutex
	pending map[int64]chan *Message
	handler Handler
	done    chan struct{}
	errMu   sync.Mutex
	err     error
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewConn starts reading from r. Incoming requests are handled
// concurrently by h.
func NewConn(r io.Reader, w io.Writer, h Handler) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{w: w, pending: map[int64]chan *Message{}, handler: h, done: make(chan struct{}), ctx: ctx, cancel: cancel}
	go c.readLoop(r)
	return c
}

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err returns the reason the connection ended.
func (c *Conn) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

func (c *Conn) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			continue // ignore garbage (e.g. stray prints)
		}
		switch {
		case m.Method != "" && m.ID != nil:
			go c.serve(m)
		case m.Method != "":
			if c.handler != nil {
				go func() { _, _ = c.handler(c.ctx, m.Method, m.Params) }()
			}
		case m.ID != nil:
			c.pmu.Lock()
			ch := c.pending[*m.ID]
			delete(c.pending, *m.ID)
			c.pmu.Unlock()
			if ch != nil {
				msg := m
				ch <- &msg
			}
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.errMu.Lock()
	c.err = err
	c.errMu.Unlock()
	c.cancel()
	c.pmu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.pmu.Unlock()
	close(c.done)
}

func (c *Conn) serve(m Message) {
	resp := Message{JSONRPC: "2.0", ID: m.ID}
	if c.handler == nil {
		resp.Error = &Error{Code: CodeMethodNotFound, Message: "no handler"}
	} else {
		res, err := c.handler(c.ctx, m.Method, m.Params)
		if err != nil {
			var re *Error
			if errors.As(err, &re) {
				resp.Error = re
			} else {
				resp.Error = &Error{Code: CodeInternal, Message: err.Error()}
			}
		} else {
			data, err := json.Marshal(res)
			if err != nil {
				resp.Error = &Error{Code: CodeInternal, Message: err.Error()}
			} else {
				resp.Result = data
			}
		}
	}
	_ = c.write(resp)
}

func (c *Conn) write(m Message) error {
	m.JSONRPC = "2.0"
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(append(data, '\n'))
	return err
}

// Call sends a request and waits for the response. result may be nil.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan *Message, 1)
	c.pmu.Lock()
	select {
	case <-c.done:
		c.pmu.Unlock()
		return ErrClosed
	default:
	}
	c.pending[id] = ch
	c.pmu.Unlock()
	if err := c.write(Message{ID: &id, Method: method, Params: raw}); err != nil {
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	case <-ctx.Done():
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		return ctx.Err()
	}
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(Message{Method: method, Params: raw})
}
