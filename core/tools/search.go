package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// errStop ends a walk early without an error.
var errStop = errors.New("stop")

// vcsDirs are never walked.
var vcsDirs = map[string]bool{".git": true, ".hg": true, ".svn": true}

// walker walks a directory tree as the search tools see it: version
// control directories are skipped, and so are paths matched by
// .git/info/exclude and by .gitignore and .ignore files from the
// repository root down. Hidden files are included.
type walker struct {
	// root is the repository root, or the search directory outside one.
	root  string
	cache map[string][]gitignore.Pattern
}

func newWalker(dir string) *walker {
	return &walker{root: repoRoot(dir), cache: map[string][]gitignore.Pattern{}}
}

// repoRoot is the nearest of dir and its ancestors that contains .git, or
// dir when there is none.
func repoRoot(dir string) string {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}

// parts splits p into its path elements relative to the root.
func (w *walker) parts(p string) []string {
	rel, err := filepath.Rel(w.root, p)
	if err != nil || rel == "." {
		return nil
	}
	return strings.Split(filepath.ToSlash(rel), "/")
}

// patterns returns the ignore patterns that apply inside dir, in
// increasing priority.
func (w *walker) patterns(dir string) []gitignore.Pattern {
	if ps, ok := w.cache[dir]; ok {
		return ps
	}
	var ps []gitignore.Pattern
	if dir == w.root {
		ps = readIgnoreFile(filepath.Join(dir, ".git", "info", "exclude"), nil)
	} else if parent := filepath.Dir(dir); parent != dir {
		ps = slices.Clip(w.patterns(parent))
	}
	domain := w.parts(dir)
	ps = append(ps, readIgnoreFile(filepath.Join(dir, ".gitignore"), domain)...)
	ps = append(ps, readIgnoreFile(filepath.Join(dir, ".ignore"), domain)...)
	w.cache[dir] = ps
	return ps
}

func readIgnoreFile(name string, domain []string) []gitignore.Pattern {
	f, err := os.Open(name)
	if err != nil {
		return nil
	}
	defer f.Close()
	var ps []gitignore.Pattern
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ps = append(ps, gitignore.ParsePattern(line, domain))
	}
	return ps
}

func (w *walker) ignored(p string, isDir bool) bool {
	return gitignore.NewMatcher(w.patterns(filepath.Dir(p))).Match(w.parts(p), isDir)
}

// walk calls fn for every file and directory below dir, in lexical order,
// skipping what the walker ignores. Symbolic links are passed to fn but
// never followed, and unreadable entries are skipped. fn may return
// errStop to end the walk.
func (w *walker) walk(ctx context.Context, dir string, fn func(p string, d fs.DirEntry) error) error {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			return nil
		}
		if p == dir {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if vcsDirs[d.Name()] || w.ignored(p, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return fn(p, d)
	})
	if errors.Is(err, errStop) {
		return nil
	}
	return err
}

// glob matches paths, relative to the search directory and with forward
// slashes, against a pattern. A pattern without a slash matches the name
// at any depth, as in rg and fd.
type glob struct {
	pattern string
	name    bool
}

func newGlob(p string) (glob, error) {
	p = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(p)), "./")
	if p == "" || !doublestar.ValidatePattern(p) {
		return glob{}, fmt.Errorf("invalid glob %q", p)
	}
	return glob{pattern: p, name: !strings.Contains(p, "/")}, nil
}

func (g glob) match(rel string) bool {
	if g.name {
		rel = path.Base(rel)
	}
	ok, _ := doublestar.Match(g.pattern, rel)
	return ok
}

// relSlash is p relative to dir, with forward slashes.
func relSlash(dir, p string) string {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(rel)
}

// searchDir resolves a search tool's path argument (default: the working
// directory) and checks that it exists.
func searchDir(env *Env, p string) (string, fs.FileInfo, error) {
	if strings.TrimSpace(p) == "" {
		p = "."
	}
	abs := ResolvePath(env.Cwd, p)
	info, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return abs, nil, fmt.Errorf("path not found: %s", p)
	}
	if err != nil {
		return abs, nil, fmt.Errorf("cannot read %s: %w", p, err)
	}
	return abs, info, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
