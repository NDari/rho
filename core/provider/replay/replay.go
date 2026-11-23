// Package replay serves recorded provider responses over HTTP for tests,
// with the faults seen in real use injected: streams cut off by a proxy,
// connections dropped mid-response, servers that stall, and error
// replies with Retry-After headers.
//
// The recordings directory holds raw response bodies captured from real
// servers (see recordings/README.md); Recording returns them.
package replay

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"sync"
	"time"
)

//go:embed recordings
var recordings embed.FS

// Recording returns the recorded body with the given file name. It panics
// if there is none, which is a mistake in the test.
func Recording(name string) []byte {
	b, err := recordings.ReadFile(path.Join("recordings", name))
	if err != nil {
		panic(fmt.Sprintf("replay: no recording %q", name))
	}
	return b
}

// Events splits an SSE body into its events, each with its trailing blank
// line, so a body can be cut at event boundaries.
func Events(body []byte) [][]byte {
	var out [][]byte
	for len(body) > 0 {
		i := bytes.Index(body, []byte("\n\n"))
		if i < 0 {
			out = append(out, body)
			break
		}
		out = append(out, body[:i+2])
		body = body[i+2:]
	}
	return out
}

// Reply is what the server does for one request.
type Reply struct {
	// Status defaults to 200, which sends Body as an event stream;
	// other statuses send it as JSON.
	Status int
	Header map[string]string
	Body   []byte
	// CutAt, if positive, sends only the first CutAt bytes of Body, and
	// if negative none of it. The response then ends cleanly, as when a
	// proxy closes the stream, unless Abort or Stall is set.
	CutAt int
	// Abort drops the connection after the bytes sent, without ending
	// the response, as a network failure does.
	Abort bool
	// Stall stops sending after the bytes sent and keeps the connection
	// open until the client gives up.
	Stall bool
	// Gap, if set, is a pause before each event, as a slow server makes.
	Gap time.Duration
}

// Server replays one Reply per request, in order.
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	replies  []Reply
	requests []map[string]any
}

// New starts a server. Close it when done.
func New(replies ...Reply) *Server {
	s := &Server{replies: replies}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Add queues more replies.
func (s *Server) Add(replies ...Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = append(s.replies, replies...)
}

// Requests returns the decoded JSON bodies of the requests so far.
func (s *Server) Requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	s.requests = append(s.requests, body)
	if len(s.replies) == 0 {
		s.mu.Unlock()
		http.Error(w, `{"error":{"message":"replay: no more replies"}}`, http.StatusTeapot)
		return
	}
	rep := s.replies[0]
	s.replies = s.replies[1:]
	s.mu.Unlock()

	status := rep.Status
	if status == 0 {
		status = http.StatusOK
	}
	if status == http.StatusOK {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	for k, v := range rep.Header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	data := rep.Body
	switch {
	case rep.CutAt < 0:
		data = nil
	case rep.CutAt > 0 && rep.CutAt < len(data):
		data = data[:rep.CutAt]
	}
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	if rep.Gap > 0 {
		for _, ev := range Events(data) {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(rep.Gap):
			}
			_, _ = w.Write(ev)
			flush()
		}
	} else {
		_, _ = w.Write(data)
		flush()
	}
	switch {
	case rep.Stall:
		<-r.Context().Done()
	case rep.Abort:
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	}
}
