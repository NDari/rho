package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"0.3.0", "0.2.0", true},
		{"0.2.0", "0.3.0", false},
		{"0.2.0", "0.2.0", false},
		{"v0.10.0", "0.9.9", true},
		{"1.0.0", "0.99.99", true},
		{"0.2.0", "0.2.0-rc1", true},
		{"0.2.0-rc1", "0.2.0", false},
		{"0.2.1", "0.2.1-0.20261119000000-abcdef123456", true},
		{"0.2.0", "0.2.1-0.20261119000000-abcdef123456", false},
		{"", "0.1.0", false},
	}
	for _, tt := range tests {
		if got := Newer(tt.a, tt.b); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestDetectAndNotice(t *testing.T) {
	cur := Detect("0.3.0")
	if cur != (Current{Version: "0.3.0", Method: Archive}) {
		t.Fatalf("Detect = %+v", cur)
	}
	if got := cur.Notice("0.4.0"); !strings.Contains(got, "version 0.4.0") || !strings.Contains(got, "'rho update'") {
		t.Errorf("Notice = %q", got)
	}
	gi := Current{Version: "0.3.0", Method: GoInstall}
	if got := gi.Notice("0.4.0"); !strings.Contains(got, GoInstallCmd) {
		t.Errorf("Notice = %q", got)
	}
	// Test binaries carry no module version.
	if cur := Detect("dev"); cur.Method != Dev {
		t.Errorf("Detect(dev) = %+v", cur)
	}
}

// server fakes the GitHub API and release downloads for a v0.3.0
// release.
type server struct {
	*httptest.Server
	files  map[string][]byte
	sums   map[string]string // overrides the checksums
	checks atomic.Int32
}

func newServer(t *testing.T, files map[string][]byte) *server {
	s := &server{files: files, sums: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/"+Repo+"/releases/latest":
			s.checks.Add(1)
			type asset struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			}
			var assets []asset
			for name := range s.files {
				assets = append(assets, asset{name, s.URL + "/dl/" + name})
			}
			assets = append(assets, asset{"checksums.txt", s.URL + "/dl/checksums.txt"})
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.3.0", "assets": assets})
		case r.URL.Path == "/dl/checksums.txt":
			for name, b := range s.files {
				sum := sha256.Sum256(b)
				h := hex.EncodeToString(sum[:])
				if o, ok := s.sums[name]; ok {
					h = o
				}
				fmt.Fprintf(w, "%s  %s\n", h, name)
			}
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			b, ok := s.files[strings.TrimPrefix(r.URL.Path, "/dl/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// checker checks for updates to version 0.2.0.
func (s *server) checker(home string) *Checker {
	return &Checker{Current: Current{Version: "0.2.0", Method: Archive}, Home: home, API: s.URL}
}

func TestCheckCachesForADay(t *testing.T) {
	s := newServer(t, nil)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	c := s.checker(t.TempDir())
	c.Now = func() time.Time { return now }

	if got := c.Cached(); got != "" {
		t.Fatalf("Cached before any check = %q", got)
	}
	for i, step := range []time.Duration{0, time.Hour, 23 * time.Hour} {
		now = now.Add(step)
		got, err := c.Check(context.Background())
		if err != nil || got != "0.3.0" {
			t.Fatalf("check %d: %q, %v", i, got, err)
		}
	}
	if n := s.checks.Load(); n != 2 {
		t.Errorf("GitHub asked %d times, want 2 (first check, then after a day)", n)
	}
	if got := c.Cached(); got != "0.3.0" {
		t.Errorf("Cached = %q", got)
	}

	// Once up to date, nothing is reported.
	c.Current.Version = "0.3.0"
	if got := c.Cached(); got != "" {
		t.Errorf("Cached when up to date = %q", got)
	}
}

// archive builds a release archive for this platform.
func archive(t *testing.T, name string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if strings.HasSuffix(name, ".zip") {
		zw := zip.NewWriter(&buf)
		for n, body := range files {
			h := &zip.FileHeader{Name: n, Method: zip.Deflate}
			h.SetMode(0o755)
			if strings.HasSuffix(n, "/") {
				h.SetMode(os.ModeDir | 0o755)
			}
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(body))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for n, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func installed(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, exeName(n)), []byte("old "+n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInstallReplacesProgramsNextToEachOther(t *testing.T) {
	dir := t.TempDir()
	installed(t, dir, "rho", "rho-gui")
	name := ArchiveName("0.3.0")
	s := newServer(t, map[string][]byte{name: archive(t, name, map[string]string{
		exeName("rho"):     "new rho",
		exeName("rho-gui"): "new rho-gui",
		"LICENSE":          "license",
	})})
	c := s.checker(t.TempDir())
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Install(context.Background(), rel, filepath.Join(dir, exeName("rho")))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Notes) > 0 {
		t.Errorf("unexpected notes: %v", res.Notes)
	}
	if got, err := filepath.EvalSymlinks(res.Exe); err != nil || got != mustEval(t, filepath.Join(dir, exeName("rho"))) {
		t.Errorf("Exe = %q", res.Exe)
	}
	for _, n := range []string{"rho", "rho-gui"} {
		if got := readFile(t, filepath.Join(dir, exeName(n))); got != "new "+n {
			t.Errorf("%s = %q", n, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "LICENSE")); err == nil {
		t.Error("files other than the programs were unpacked")
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	q, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestInstallOnlyTouchesProgramsThatAreThere(t *testing.T) {
	dir := t.TempDir()
	installed(t, dir, "rho")
	name := ArchiveName("0.3.0")
	s := newServer(t, map[string][]byte{name: archive(t, name, map[string]string{
		exeName("rho"):     "new rho",
		exeName("rho-gui"): "new rho-gui",
	})})
	c := s.checker(t.TempDir())
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Install(context.Background(), rel, filepath.Join(dir, exeName("rho"))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, exeName("rho-gui"))); err == nil {
		t.Error("rho-gui was added")
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	dir := t.TempDir()
	installed(t, dir, "rho")
	name := ArchiveName("0.3.0")
	s := newServer(t, map[string][]byte{name: archive(t, name, map[string]string{exeName("rho"): "evil"})})
	s.sums[name] = strings.Repeat("0", 64)
	c := s.checker(t.TempDir())
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Install(context.Background(), rel, filepath.Join(dir, exeName("rho")))
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want a checksum error", err)
	}
	if got := readFile(t, filepath.Join(dir, exeName("rho"))); got != "old rho" {
		t.Errorf("rho changed to %q", got)
	}
}

func TestInstallMissingPlatform(t *testing.T) {
	dir := t.TempDir()
	installed(t, dir, "rho")
	s := newServer(t, map[string][]byte{"rho_0.3.0_plan9_amd64.tar.gz": nil})
	c := s.checker(t.TempDir())
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Install(context.Background(), rel, filepath.Join(dir, exeName("rho"))); err == nil || !strings.Contains(err.Error(), "no download for this platform") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallFallsBackWhenNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a read-only directory")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	installed(t, dir, "rho", "rho-gui")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	t.Setenv("PATH", dir)

	name := ArchiveName("0.3.0")
	s := newServer(t, map[string][]byte{name: archive(t, name, map[string]string{
		"rho": "new rho", "rho-gui": "new rho-gui",
	})})
	c := s.checker(t.TempDir())
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Install(context.Background(), rel, filepath.Join(dir, "rho"))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".local", "bin")
	if res.Exe != filepath.Join(bin, "rho") {
		t.Errorf("Exe = %q", res.Exe)
	}
	for _, n := range []string{"rho", "rho-gui"} {
		if got := readFile(t, filepath.Join(bin, n)); got != "new "+n {
			t.Errorf("%s = %q", n, got)
		}
		if got := readFile(t, filepath.Join(dir, n)); got != "old "+n {
			t.Errorf("old %s = %q", n, got)
		}
	}
	notes := strings.Join(res.Notes, "\n")
	if !strings.Contains(notes, "not writable") || !strings.Contains(notes, "Add "+bin+" to your PATH") {
		t.Errorf("notes = %q", notes)
	}

	// With the new folder later on PATH, the old copy still wins.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+bin)
	if res, err = c.Install(context.Background(), rel, filepath.Join(dir, "rho")); err != nil {
		t.Fatal(err)
	}
	if notes := strings.Join(res.Notes, "\n"); !strings.Contains(notes, "comes before") {
		t.Errorf("notes = %q", notes)
	}
}

func TestInstallApp(t *testing.T) {
	parent := t.TempDir()
	app := filepath.Join(parent, "Rho.app")
	macos := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(macos, "rho-gui"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(macos, "stale"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	name := AppName("0.3.0")
	s := newServer(t, map[string][]byte{name: archive(t, name, map[string]string{
		"Rho.app/":                       "",
		"Rho.app/Contents/":              "",
		"Rho.app/Contents/MacOS/":        "",
		"Rho.app/Contents/MacOS/rho-gui": "new rho-gui",
		"Rho.app/Contents/MacOS/rho":     "new rho",
		"Rho.app/Contents/Info.plist":    "plist",
	})})
	c := s.checker(t.TempDir())
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.installApp(context.Background(), rel, app, "rho-gui")
	if err != nil {
		t.Fatal(err)
	}
	if res.App != app || res.Exe != filepath.Join(macos, "rho-gui") {
		t.Errorf("result = %+v", res)
	}
	if got := readFile(t, filepath.Join(macos, "rho")); got != "new rho" {
		t.Errorf("rho = %q", got)
	}
	if _, err := os.Stat(filepath.Join(macos, "stale")); err == nil {
		t.Error("the old bundle's files remain")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Join(macos, "rho")); err != nil || info.Mode().Perm()&0o100 == 0 {
			t.Errorf("rho is not executable: %v %v", info.Mode(), err)
		}
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 1 {
		t.Errorf("leftovers next to the app: %v", entries)
	}
}

func TestAppBundle(t *testing.T) {
	got := appBundle(filepath.FromSlash("/Applications/Rho.app/Contents/MacOS/rho-gui"))
	want := ""
	if runtime.GOOS == "darwin" {
		want = "/Applications/Rho.app"
	}
	if got != want {
		t.Errorf("appBundle = %q, want %q", got, want)
	}
	if got := appBundle(filepath.FromSlash("/usr/local/bin/rho")); got != "" {
		t.Errorf("appBundle = %q", got)
	}
}
