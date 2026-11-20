//go:build !windows

package update

import (
	"os"
	"path/filepath"
)

const archiveExt = ".tar.gz"

func exeName(name string) string { return name }

// fallbackDir is where an update goes when the installed copy's folder is
// not writable.
func fallbackDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin"), nil
}

func samePath(a, b string) bool { return a == b }
