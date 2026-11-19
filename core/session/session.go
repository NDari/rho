// Package session stores conversations as an append-only JSONL tree.
package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NDari/rho/core/provider"
)

// FormatVersion is the session file format version.
const FormatVersion = 1

// EntryType identifies the kind of entry.
type EntryType string

// Entry types.
const (
	TypeHeader       EntryType = "header"
	TypeMessage      EntryType = "message"
	TypeModelChange  EntryType = "model_change"
	TypeCompaction   EntryType = "compaction"
	TypeCheckout     EntryType = "checkout"
	TypeAsideStart   EntryType = "aside_start"
	TypeAsideSummary EntryType = "aside_summary"
	TypeLabel        EntryType = "label"
)

// Entry is one line of a session file.
type Entry struct {
	ID     string    `json:"id"`
	Parent string    `json:"parent,omitempty"`
	Type   EntryType `json:"type"`
	TS     time.Time `json:"ts"`

	// header
	Version    int    `json:"version,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	RhoVersion string `json:"rho_version,omitempty"`
	// ForkedFrom records the source session and entry for /fork.
	ForkedFrom string `json:"forked_from,omitempty"`

	// message
	Message *provider.Message `json:"message,omitempty"`

	// model_change
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	// compaction
	Summary      string `json:"summary,omitempty"`
	FirstKept    string `json:"first_kept,omitempty"`
	TokensBefore int    `json:"tokens_before,omitempty"`

	// checkout and label
	Target string `json:"target,omitempty"`
	Label  string `json:"label,omitempty"`

	// aside_summary
	Note string `json:"note,omitempty"`

	// Display is presentation data for tool results (e.g. diffs), kept so
	// frontends can redraw resumed sessions. Never sent to the model.
	Display json.RawMessage `json:"display,omitempty"`
}

// inTree reports whether the entry is a node of the conversation tree.
// Checkouts, labels and the header are metadata.
func (e *Entry) inTree() bool {
	switch e.Type {
	case TypeHeader, TypeCheckout, TypeLabel:
		return false
	}
	return true
}

// Session is an open session. It is safe for concurrent use.
type Session struct {
	mu      sync.Mutex
	path    string // empty for in-memory sessions
	file    *os.File
	header  Entry
	entries []*Entry
	byID    map[string]*Entry
	leaf    string
	labels  map[string]string
}

// New creates a session. If dir is non-empty the file is created there
// lazily, on the first appended entry, so empty sessions leave no files.
func New(dir, cwd, rhoVersion string) *Session {
	s := &Session{byID: map[string]*Entry{}, labels: map[string]string{}}
	now := time.Now().UTC()
	s.header = Entry{ID: randomID(), Type: TypeHeader, TS: now, Version: FormatVersion, Cwd: cwd, RhoVersion: rhoVersion}
	if dir != "" {
		name := now.Format("20060102T150405Z") + "_" + s.header.ID + ".jsonl"
		s.path = filepath.Join(dir, name)
	}
	return s
}

// Open loads a session file.
func Open(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := &Session{path: path, byID: map[string]*Entry{}, labels: map[string]string{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 256*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			// A torn final line (e.g. after a crash) is tolerated.
			if !sc.Scan() {
				break
			}
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if line == 1 && e.Type == TypeHeader {
			s.header = e
			continue
		}
		s.apply(&e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if s.header.Type != TypeHeader {
		return nil, fmt.Errorf("%s: missing session header", path)
	}
	return s, nil
}

func (s *Session) apply(e *Entry) {
	s.entries = append(s.entries, e)
	s.byID[e.ID] = e
	switch e.Type {
	case TypeCheckout:
		s.leaf = e.Target
	case TypeLabel:
		if e.Label == "" {
			delete(s.labels, e.Target)
		} else {
			s.labels[e.Target] = e.Label
		}
	default:
		s.leaf = e.ID
	}
}

// ID returns the session id.
func (s *Session) ID() string { return s.header.ID }

// Path returns the session file path (empty for in-memory sessions).
func (s *Session) Path() string { return s.path }

// Header returns the session header.
func (s *Session) Header() Entry { return s.header }

// Leaf returns the current position.
func (s *Session) Leaf() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaf
}

// Get returns an entry by id.
func (s *Session) Get(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[id]
	if !ok {
		return Entry{}, false
	}
	return *e, true
}

// Entries returns a copy of all entries in file order.
func (s *Session) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	for i, e := range s.entries {
		out[i] = *e
	}
	return out
}

// Labels returns entry labels.
func (s *Session) Labels() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.labels))
	for k, v := range s.labels {
		out[k] = v
	}
	return out
}

// Append adds an entry. ID and TS are filled in; Parent defaults to the
// current leaf for tree entries.
func (s *Session) Append(e Entry) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.ID = s.newID()
	e.TS = time.Now().UTC()
	if e.inTree() && e.Parent == "" {
		e.Parent = s.leaf
	}
	if e.Type == TypeCheckout || e.Type == TypeLabel {
		if _, ok := s.byID[e.Target]; !ok && e.Target != "" {
			return Entry{}, fmt.Errorf("unknown entry %q", e.Target)
		}
	}
	if err := s.write(&e); err != nil {
		return Entry{}, err
	}
	ec := e
	s.apply(&ec)
	return e, nil
}

// AppendMessage appends a message entry.
func (s *Session) AppendMessage(m provider.Message) (Entry, error) {
	return s.Append(Entry{Type: TypeMessage, Message: &m})
}

// Checkout moves the current position to target ("" means before the
// first entry).
func (s *Session) Checkout(target string) error {
	_, err := s.Append(Entry{Type: TypeCheckout, Target: target})
	return err
}

func (s *Session) write(e *Entry) error {
	if s.path == "" {
		return nil
	}
	if s.file == nil {
		if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		s.file = f
		info, err := f.Stat()
		if err == nil && info.Size() == 0 {
			if err := writeLine(f, &s.header); err != nil {
				return err
			}
		}
	}
	return writeLine(s.file, e)
}

func writeLine(f *os.File, e *Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = f.Write(data)
	return err
}

// Close closes the session file.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		err := s.file.Close()
		s.file = nil
		return err
	}
	return nil
}

func (s *Session) newID() string {
	for {
		id := randomID()
		if _, ok := s.byID[id]; !ok && id != s.header.ID {
			return id
		}
	}
}

func randomID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// PathTo returns the tree entries from the root to id (inclusive).
func (s *Session) PathTo(id string) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pathTo(id)
}

func (s *Session) pathTo(id string) []Entry {
	var rev []Entry
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		e, ok := s.byID[id]
		if !ok {
			break
		}
		rev = append(rev, *e)
		id = e.Parent
	}
	out := make([]Entry, len(rev))
	for i := range rev {
		out[i] = rev[len(rev)-1-i]
	}
	return out
}

// Children returns the tree children of id in file order.
func (s *Session) Children(id string) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Entry
	for _, e := range s.entries {
		if e.inTree() && e.Parent == id {
			out = append(out, *e)
		}
	}
	return out
}

// ErrNoEntry is returned when an entry id is unknown.
var ErrNoEntry = errors.New("no such entry")
