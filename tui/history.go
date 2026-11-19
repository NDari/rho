package tui

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

const historyMax = 500

// history stores submitted prompts in ~/.rho/history.jsonl.
type history struct {
	path    string
	entries []string
	pos     int // len(entries) means "not browsing"
	draft   string
}

func loadHistory(home string) *history {
	h := &history{path: filepath.Join(home, "history.jsonl")}
	f, err := os.Open(h.path)
	if err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			var s string
			if json.Unmarshal(sc.Bytes(), &s) == nil && s != "" {
				h.entries = append(h.entries, s)
			}
		}
		f.Close()
	}
	if len(h.entries) > historyMax {
		h.entries = h.entries[len(h.entries)-historyMax:]
	}
	h.pos = len(h.entries)
	return h
}

func (h *history) add(s string) {
	if s == "" || (len(h.entries) > 0 && h.entries[len(h.entries)-1] == s) {
		h.pos = len(h.entries)
		return
	}
	h.entries = append(h.entries, s)
	h.pos = len(h.entries)
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	data, _ := json.Marshal(s)
	_, _ = f.Write(append(data, '\n'))
}

// prev returns the previous entry, saving the current draft first.
func (h *history) prev(current string) (string, bool) {
	if h.pos == 0 || len(h.entries) == 0 {
		return "", false
	}
	if h.pos == len(h.entries) {
		h.draft = current
	}
	h.pos--
	return h.entries[h.pos], true
}

func (h *history) next() (string, bool) {
	if h.pos >= len(h.entries) {
		return "", false
	}
	h.pos++
	if h.pos == len(h.entries) {
		return h.draft, true
	}
	return h.entries[h.pos], true
}
