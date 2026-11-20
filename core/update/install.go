package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/creativeprojects/go-selfupdate/update"
)

// programs are the binaries a release archive holds.
var programs = []string{"rho", "rho-gui"}

// maxDownload caps the size of a downloaded file.
const maxDownload = 512 << 20

// Result describes an installed update.
type Result struct {
	// Version is the installed version.
	Version string
	// Exe is the new copy of the program that called Install.
	Exe string
	// App is the new Rho.app, when the program runs from one (macOS).
	App string
	// Notes tell the user about anything left to do, such as adding a
	// folder to PATH.
	Notes []string
}

// Install downloads rel and replaces the installation exe belongs to:
// Rho.app when exe runs from it, else exe and the other Rho programs next
// to it. When that folder is not writable, the new version goes to a
// folder in the user's home instead and Result.Notes says so.
func (c *Checker) Install(ctx context.Context, rel *Release, exe string) (Result, error) {
	exe, err := filepath.Abs(exe)
	if err != nil {
		return Result{}, err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	if app := appBundle(exe); app != "" {
		return c.installApp(ctx, rel, app, filepath.Base(exe))
	}
	return c.installPrograms(ctx, rel, exe)
}

// ArchiveName is the release archive for this platform.
func ArchiveName(version string) string {
	return fmt.Sprintf("rho_%s_%s_%s%s", version, runtime.GOOS, runtime.GOARCH, archiveExt)
}

// AppName is the release file holding Rho.app.
func AppName(version string) string { return fmt.Sprintf("Rho_%s_macos.zip", version) }

func (c *Checker) installPrograms(ctx context.Context, rel *Release, exe string) (Result, error) {
	dir := filepath.Dir(exe)
	self := filepath.Base(exe)
	var want []string
	for _, p := range programs {
		name := exeName(p)
		if name == self || fileExists(filepath.Join(dir, name)) {
			want = append(want, name)
		}
	}
	if !slices.Contains(want, self) {
		want = append(want, self)
	}

	asset := ArchiveName(rel.Version)
	data, err := c.download(ctx, rel, asset)
	if err != nil {
		return Result{}, err
	}
	files, err := extract(data, asset, want)
	if err != nil {
		return Result{}, err
	}
	if _, ok := files[self]; !ok {
		return Result{}, fmt.Errorf("%s has no %s", asset, self)
	}

	res := Result{Version: rel.Version}
	target := dir
	if !writable(dir) {
		if target, err = fallbackDir(); err != nil {
			return Result{}, err
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return Result{}, err
		}
		res.Notes = append(res.Notes, fallbackNotes(dir, target, rel.Version)...)
	}
	for _, name := range want {
		b, ok := files[name]
		if !ok {
			res.Notes = append(res.Notes, fmt.Sprintf("%s was not updated: the release has no %s for this platform.", filepath.Join(dir, name), name))
			continue
		}
		if err := replace(filepath.Join(target, name), b); err != nil {
			return Result{}, err
		}
	}
	res.Exe = filepath.Join(target, self)
	return res, nil
}

func (c *Checker) installApp(ctx context.Context, rel *Release, app, self string) (Result, error) {
	asset := AppName(rel.Version)
	data, err := c.download(ctx, rel, asset)
	if err != nil {
		return Result{}, err
	}
	res := Result{Version: rel.Version}
	parent := filepath.Dir(app)
	if !writable(parent) {
		home, err := os.UserHomeDir()
		if err != nil {
			return Result{}, err
		}
		target := filepath.Join(home, "Applications")
		if err := os.MkdirAll(target, 0o755); err != nil {
			return Result{}, err
		}
		res.Notes = append(res.Notes, fmt.Sprintf("%s is not writable, so Rho %s was installed to %s instead. The old Rho.app in %s was left as it is.", parent, rel.Version, target, parent))
		parent = target
	}
	tmp, err := os.MkdirTemp(parent, ".rho-update-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmp)
	if err := unzipAll(data, tmp); err != nil {
		return Result{}, fmt.Errorf("unpack %s: %w", asset, err)
	}
	fresh := filepath.Join(tmp, "Rho.app")
	if !fileExists(filepath.Join(fresh, "Contents", "MacOS", self)) {
		return Result{}, fmt.Errorf("%s has no Rho.app/Contents/MacOS/%s", asset, self)
	}
	dest := filepath.Join(parent, filepath.Base(app))
	old := filepath.Join(tmp, "old.app")
	hadOld := fileExists(dest)
	if hadOld {
		if err := os.Rename(dest, old); err != nil {
			return Result{}, err
		}
	}
	if err := os.Rename(fresh, dest); err != nil {
		if hadOld {
			_ = os.Rename(old, dest)
		}
		return Result{}, err
	}
	res.App = dest
	res.Exe = filepath.Join(dest, "Contents", "MacOS", self)
	return res, nil
}

// fallbackNotes explain an install to fallback instead of dir.
func fallbackNotes(dir, fallback, version string) []string {
	notes := []string{fmt.Sprintf("%s is not writable, so Rho %s was installed to %s instead. The old copy in %s was left as it is; remove it once the new one works.", dir, version, fallback, dir)}
	newAt, oldAt := pathIndex(fallback), pathIndex(dir)
	switch {
	case newAt < 0:
		notes = append(notes, fmt.Sprintf("Add %s to your PATH.", fallback))
	case oldAt >= 0 && oldAt < newAt:
		notes = append(notes, fmt.Sprintf("%s comes before %s on your PATH, so the old copy still runs; remove it or put %s first.", dir, fallback, fallback))
	}
	return notes
}

// pathIndex returns the position of dir in PATH, or -1.
func pathIndex(dir string) int {
	for i, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p != "" && samePath(filepath.Clean(p), filepath.Clean(dir)) {
			return i
		}
	}
	return -1
}

// download fetches a release file and checks it against checksums.txt.
func (c *Checker) download(ctx context.Context, rel *Release, name string) ([]byte, error) {
	if rel.Assets[name] == "" {
		return nil, fmt.Errorf("release %s has no %s (no download for this platform)", rel.Version, name)
	}
	sums, err := c.fetch(ctx, rel, "checksums.txt")
	if err != nil {
		return nil, err
	}
	want, err := checksum(sums, name)
	if err != nil {
		return nil, err
	}
	data, err := c.fetch(ctx, rel, name)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("%s does not match its checksum; the download may be damaged", name)
	}
	return data, nil
}

func (c *Checker) fetch(ctx context.Context, rel *Release, name string) ([]byte, error) {
	url := rel.Assets[name]
	if url == "" {
		return nil, fmt.Errorf("release %s has no %s", rel.Version, name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "rho/"+c.Current.Version)
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", name, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	if len(data) > maxDownload {
		return nil, fmt.Errorf("download %s: too large", name)
	}
	return data, nil
}

// checksum finds name's SHA-256 in a checksums.txt file.
func checksum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", name)
}

// extract returns the files at the top of a .tar.gz or .zip archive whose
// names are in want.
func extract(data []byte, asset string, want []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	keep := func(name string) bool { return slices.Contains(want, path.Clean(name)) }
	if strings.HasSuffix(asset, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("unpack %s: %w", asset, err)
		}
		for _, f := range zr.File {
			if !f.Mode().IsRegular() || !keep(f.Name) {
				continue
			}
			b, err := readZip(f)
			if err != nil {
				return nil, fmt.Errorf("unpack %s: %w", asset, err)
			}
			out[path.Clean(f.Name)] = b
		}
		return out, nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("unpack %s: %w", asset, err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("unpack %s: %w", asset, err)
		}
		if h.Typeflag != tar.TypeReg || !keep(h.Name) {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxDownload))
		if err != nil {
			return nil, fmt.Errorf("unpack %s: %w", asset, err)
		}
		out[path.Clean(h.Name)] = b
	}
}

func readZip(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxDownload))
}

// unzipAll unpacks a zip archive into dir, keeping file modes and
// symbolic links.
func unzipAll(data []byte, dir string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		if !filepath.IsLocal(f.Name) {
			return fmt.Errorf("unsafe path %q", f.Name)
		}
		dst := filepath.Join(dir, filepath.FromSlash(f.Name))
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			target, err := readZip(f)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(string(target), dst); err != nil {
				return err
			}
		default:
			b, err := readZip(f)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(dst, b, mode.Perm()|0o200); err != nil {
				return err
			}
		}
	}
	return nil
}

// replace puts a program at p, swapping it in when p exists (which also
// works for a running program on Windows).
func replace(p string, b []byte) error {
	if fileExists(p) {
		err := update.Apply(bytes.NewReader(b), update.Options{TargetPath: p, TargetMode: 0o755})
		if rerr := update.RollbackError(err); rerr != nil {
			return fmt.Errorf("replace %s: %w; restoring the old copy also failed: %w", p, err, rerr)
		}
		if err != nil {
			return fmt.Errorf("replace %s: %w", p, err)
		}
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".new-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o755)); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %w", p, err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// writable reports whether files can be created in dir.
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".rho-write-test-")
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	return true
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
