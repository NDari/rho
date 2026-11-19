package ext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// TrustStore remembers approved project extensions by content hash.
type TrustStore struct {
	mu   sync.Mutex
	path string
	data trustFile
}

type trustFile struct {
	Extensions map[string]string `json:"extensions"`
}

// LoadTrust reads ~/.rho/trust.json.
func LoadTrust(home string) *TrustStore {
	t := &TrustStore{path: filepath.Join(home, "trust.json"), data: trustFile{Extensions: map[string]string{}}}
	if b, err := os.ReadFile(t.path); err == nil {
		_ = json.Unmarshal(b, &t.data)
		if t.data.Extensions == nil {
			t.data.Extensions = map[string]string{}
		}
	}
	return t
}

// Trusted reports whether path is approved with this hash.
func (t *TrustStore) Trusted(path, hash string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.data.Extensions[path] == hash
}

// Approve records an approval and saves the store.
func (t *TrustStore) Approve(path, hash string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.Extensions[path] = hash
	b, err := json.MarshalIndent(t.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}

// HashPath hashes a file, or every regular file under a directory (by
// relative path and content), skipping .git.
func HashPath(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if !info.IsDir() {
		if err := hashFile(h, path, filepath.Base(path)); err != nil {
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	var files []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, f := range files {
		rel, _ := filepath.Rel(path, f)
		if err := hashFile(h, f, filepath.ToSlash(rel)); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashFile(h io.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, _ = io.WriteString(h, name+"\x00")
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	_, _ = h.Write([]byte{0})
	return nil
}
