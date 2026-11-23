// Package session stores conversations as an append-only JSONL tree.
package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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
	// problems describes damaged lines that Open worked around.
	problems []string
	// tornAt and tornEnd are the byte range of an incomplete final line
	// (tornEnd is 0 if there is none); it is cut off before the next write.
	tornAt, tornEnd int64
	// size is the length of the open file.
	size int64
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

// Open loads a session file. Damage left by an interrupted write is worked
// around rather than refused: an incomplete final line is ignored (and cut
// off before the next append), a partial entry joined to the start of a
// complete one is dropped, and other unreadable lines are skipped.
// Problems describes what was found.
func Open(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := &Session{path: path, byID: map[string]*Entry{}, labels: map[string]string{}}
	r := bufio.NewReaderSize(f, 1024*1024)
	var offset int64
	for line := 1; ; line++ {
		raw, rerr := r.ReadBytes('\n')
		if rerr != nil && rerr != io.EOF {
			return nil, fmt.Errorf("%s: %w", path, rerr)
		}
		start := offset
		offset += int64(len(raw))
		if len(bytes.TrimSpace(raw)) > 0 {
			complete := rerr == nil // ends with a newline
			if err := s.load(raw, line, start, offset, complete); err != nil {
				return nil, fmt.Errorf("%s:%d: %w", path, line, err)
			}
		}
		if rerr == io.EOF {
			break
		}
	}
	if s.header.Type != TypeHeader {
		return nil, fmt.Errorf("%s: missing session header", path)
	}
	if n := s.orphans(); n > 0 {
		s.problems = append(s.problems, fmt.Sprintf("%d entries follow entries that could not be read, so the conversation is missing earlier messages", n))
	}
	return s, nil
}

// load applies one line of the file, which spans bytes start to end.
// Only an unreadable header is an error.
func (s *Session) load(raw []byte, line int, start, end int64, complete bool) error {
	e, damage, err := parseLine(raw)
	switch {
	case err != nil && line == 1:
		return err
	case err != nil && !complete:
		// The last write was interrupted by a crash.
		s.tornAt, s.tornEnd = start, end
		s.problems = append(s.problems, fmt.Sprintf("line %d: ignored an incomplete final entry left by an interrupted write", line))
		return nil
	case err != nil:
		s.problems = append(s.problems, fmt.Sprintf("line %d: skipped an unreadable entry (%v)", line, err))
		return nil
	case damage:
		s.problems = append(s.problems, fmt.Sprintf("line %d: dropped a partial entry left by an interrupted write", line))
	}
	if line == 1 && e.Type == TypeHeader {
		s.header = e
		return nil
	}
	s.apply(&e)
	return nil
}

// orphans counts tree entries whose parent is unknown.
func (s *Session) orphans() int {
	n := 0
	for _, e := range s.entries {
		if e.inTree() && e.Parent != "" && s.byID[e.Parent] == nil {
			n++
		}
	}
	return n
}

// entryStart begins every entry line (ID is the first field of Entry).
var entryStart = []byte(`{"id":"`)

// parseLine decodes one line. A write interrupted by a crash leaves a
// partial entry without a newline, and older versions appended the next
// entry right after it; damage reports that such a prefix was dropped.
func parseLine(raw []byte) (e Entry, damage bool, err error) {
	if err = json.Unmarshal(raw, &e); err == nil {
		return e, false, nil
	}
	for i := 1; i < len(raw); {
		j := bytes.Index(raw[i:], entryStart)
		if j < 0 {
			break
		}
		i += j
		var r Entry
		if json.Unmarshal(raw[i:], &r) == nil && r.ID != "" {
			return r, true, nil
		}
		i++
	}
	return Entry{}, false, err
}

// Problems describes damage that Open found and worked around, one line
// each; empty for a healthy file.
func (s *Session) Problems() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.problems)
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
		if err := s.cutTorn(); err != nil {
			return err
		}
		f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		var prefix []byte
		info, err := f.Stat()
		switch {
		case err != nil:
			_ = f.Close()
			return err
		case info.Size() == 0:
			if prefix, err = json.Marshal(&s.header); err != nil {
				_ = f.Close()
				return err
			}
			prefix = append(prefix, '\n')
		case !endsWithNewline(s.path, info.Size()):
			// An interrupted write left a partial line: start a new one so
			// the next entry is not joined to it.
			prefix = []byte{'\n'}
		}
		s.file = f
		s.size = info.Size()
		if err := s.writeBytes(prefix); err != nil {
			return err
		}
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.writeBytes(append(data, '\n'))
}

// writeBytes writes data and syncs it to disk. A failed write is undone
// as far as possible and the file closed, so the next write reopens it and
// never joins an entry to a partial line.
func (s *Session) writeBytes(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	n, err := s.file.Write(data)
	if err == nil {
		err = s.file.Sync()
	}
	if err != nil {
		if n > 0 {
			_ = s.file.Truncate(s.size)
		}
		_ = s.file.Close()
		s.file = nil
		return fmt.Errorf("writing session %s: %w", s.path, err)
	}
	s.size += int64(n)
	return nil
}

// cutTorn removes the incomplete final line Open found, if the file has
// not changed since. It was never a complete entry, so no entry is lost.
func (s *Session) cutTorn() error {
	if s.tornEnd == 0 {
		return nil
	}
	at, end := s.tornAt, s.tornEnd
	s.tornAt, s.tornEnd = 0, 0
	// If the file is gone or changed elsewhere, leave it: write then
	// starts a new line instead.
	info, statErr := os.Stat(s.path)
	if unchanged := statErr == nil && info.Size() == end; !unchanged {
		return nil
	}
	if err := os.Truncate(s.path, at); err != nil {
		return fmt.Errorf("removing the incomplete last line of %s: %w", s.path, err)
	}
	return nil
}

func endsWithNewline(path string, size int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	var b [1]byte
	if _, err := f.ReadAt(b[:], size-1); err != nil {
		return true
	}
	return b[0] == '\n'
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
