// Package docs serves the documentation and examples built into Rho: it
// lists and reads them for `rho docs`, and installs them under the Rho home
// so the model can read and search them like any other files.
package docs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/NDari/rho"
)

// placeholder in built-in files is replaced by the directory they are
// installed in.
const placeholder = "{{DOCS}}"

// Topic is a document `rho docs <name>` can print.
type Topic struct {
	Name, Path, Summary string
}

// Topics are the documents, in the order `rho docs` lists them.
var Topics = []Topic{
	{"extensions", "docs/extensions.md", "writing extensions: tools, commands, hooks and the protocol"},
	{"configuration", "docs/configuration.md", "config.toml, tool selection, and the files Rho reads"},
	{"spec", "docs/rho-spec.md", "the design spec"},
	{"gui", "docs/gui.md", "the desktop app"},
}

// Examples lists the paths of the example files.
func Examples() []string {
	var out []string
	_ = fs.WalkDir(rho.Files, "examples", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// Read returns a topic (by name) or an embedded file (by path, such as
// examples/extensions/guard.go).
func Read(name string) ([]byte, error) {
	for _, t := range Topics {
		if t.Name == name {
			name = t.Path
		}
	}
	data, err := rho.Files.ReadFile(path.Clean(strings.TrimPrefix(filepath.ToSlash(name), "./")))
	if err != nil {
		return nil, fmt.Errorf("no document %q; run rho docs to list them", name)
	}
	return data, nil
}

var (
	installMu sync.Mutex
	installed = map[string]string{}
)

// Install writes the built-in files to <home>/docs/<content hash>/ unless
// they are already there, and returns that directory. Older copies are
// removed.
func Install(home string) (string, error) {
	installMu.Lock()
	defer installMu.Unlock()
	if dir, ok := installed[home]; ok {
		return dir, nil
	}
	root := filepath.Join(home, "docs")
	dir := filepath.Join(root, hash())
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err != nil {
		if err := write(dir); err != nil {
			return "", err
		}
	}
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != filepath.Base(dir) {
				_ = os.RemoveAll(filepath.Join(root, e.Name()))
			}
		}
	}
	installed[home] = dir
	return dir, nil
}

// installPath is where an embedded file goes in an installed copy: the
// guides at the top, built-in skills as skills/<name>/SKILL.md, examples
// under examples/. A flat layout keeps the paths short and easy for a
// model to copy.
func installPath(p string) string {
	if name, ok := strings.CutPrefix(p, "docs/builtin/"); ok {
		return path.Join("skills", strings.TrimSuffix(name, ".md"), "SKILL.md")
	}
	return strings.TrimPrefix(p, "docs/")
}

// write copies every embedded file into dir, filling in the placeholder
// and fixing the guides' links to the examples, and marks the copy
// complete last.
func write(dir string) error {
	err := fs.WalkDir(rho.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := rho.Files.ReadFile(p)
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(data), placeholder, filepath.ToSlash(dir))
		if strings.HasPrefix(p, "docs/") && strings.HasSuffix(p, ".md") {
			text = strings.ReplaceAll(text, "](../examples/", "](examples/")
		}
		data = []byte(text)
		dst := filepath.Join(dir, filepath.FromSlash(installPath(p)))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		return fmt.Errorf("install the built-in docs: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, ".complete"), nil, 0o644)
}

// hash identifies the embedded content, so a new build gets a new copy.
func hash() string {
	h := sha256.New()
	_ = fs.WalkDir(rho.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := rho.Files.ReadFile(p)
			fmt.Fprintf(h, "%s\x00%d\x00", p, len(data))
			h.Write(data)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// Skills returns the paths of the built-in skills' SKILL.md files in an
// installed copy, keyed by skill directory name.
func Skills(dir string) map[string]string {
	out := map[string]string{}
	entries, _ := fs.ReadDir(rho.Files, "docs/builtin")
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".md"); ok && !e.IsDir() {
			out[name] = filepath.Join(dir, "skills", name, "SKILL.md")
		}
	}
	return out
}
