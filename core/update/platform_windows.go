//go:build windows

package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const archiveExt = ".zip"

func exeName(name string) string { return name + ".exe" }

// fallbackDir is where an update goes when the installed copy's folder is
// not writable.
func fallbackDir() (string, error) {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		return "", errors.New("LOCALAPPDATA is not set")
	}
	return filepath.Join(dir, "Programs", "Rho"), nil
}

func samePath(a, b string) bool { return strings.EqualFold(a, b) }
