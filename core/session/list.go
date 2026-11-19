package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NDari/rho/core/provider"
)

// Info summarizes a session file for pickers.
type Info struct {
	Path     string
	ID       string
	Cwd      string
	Created  time.Time
	Modified time.Time
	Title    string
	Model    string
	Messages int
	Size     int64
}

// Dir returns the session directory for a working directory.
func Dir(home, cwd string) string {
	return filepath.Join(home, "sessions", EncodeCwd(cwd))
}

// EncodeCwd turns a path into a single directory name.
func EncodeCwd(cwd string) string {
	r := strings.NewReplacer("/", "-", `\`, "-", ":", "")
	s := strings.Trim(r.Replace(cwd), "-")
	if s == "" {
		s = "root"
	}
	return "--" + s + "--"
}

// List returns sessions in dir, most recently modified first.
func List(dir string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := Summarize(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// Latest returns the most recently modified session in dir, or "".
func Latest(dir string) (string, error) {
	list, err := List(dir)
	if err != nil || len(list) == 0 {
		return "", err
	}
	return list[0].Path, nil
}

// Summarize reads the summary fields of a session file.
func Summarize(path string) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Info{}, err
	}
	info := Info{Path: path, Size: st.Size(), Modified: st.ModTime()}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 256*1024*1024)
	first := true
	var label string
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if first {
			first = false
			if e.Type == TypeHeader {
				info.ID, info.Cwd, info.Created = e.ID, e.Cwd, e.TS
				continue
			}
		}
		switch e.Type {
		case TypeMessage:
			if e.Message == nil {
				continue
			}
			info.Messages++
			if info.Title == "" && e.Message.Role == provider.RoleUser && e.Message.Meta.Synthetic == "" {
				info.Title = oneLine(e.Message.TextContent(), 100)
			}
			if e.Message.Role == provider.RoleAssistant && e.Message.Meta.Model != "" {
				info.Model = e.Message.Meta.Model
			}
		case TypeModelChange:
			info.Model = e.Model
		case TypeLabel:
			if e.Label != "" && label == "" {
				label = e.Label
			}
		}
	}
	if label != "" {
		info.Title = label
	}
	if info.Title == "" {
		info.Title = "(empty)"
	}
	return info, sc.Err()
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}
