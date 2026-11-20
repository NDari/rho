// Package update looks for new Rho releases on GitHub and installs them
// in place of the running copy.
package update

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/NDari/rho/core/config"
)

// Repo is the GitHub repository releases come from.
const Repo = "NDari/rho"

// GoInstallCmd updates a copy installed with go install.
const GoInstallCmd = "go install github.com/NDari/rho/cmd/rho@latest"

// DisableEnv turns update checks off when set to any value.
const DisableEnv = "RHO_NO_UPDATE_CHECK"

// Method says how the running program was installed.
type Method int

const (
	// Dev is a build from source without a version: no update checks.
	Dev Method = iota
	// Archive is a release archive or Rho.app: "rho update" replaces it.
	Archive
	// GoInstall is a copy built by go install, updated with go install.
	GoInstall
)

// Current describes the running program.
type Current struct {
	Version string
	Method  Method
}

// Detect works out the running program's version and how it was
// installed. version is the value set at build time, "dev" when unset.
func Detect(version string) Current {
	if version != "" && version != "dev" {
		return Current{Version: version, Method: Archive}
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		v := bi.Main.Version
		// Local builds of a modified checkout are marked +dirty.
		if v != "" && v != "(devel)" && !strings.Contains(v, "+dirty") {
			return Current{Version: strings.TrimPrefix(v, "v"), Method: GoInstall}
		}
	}
	return Current{Version: "dev", Method: Dev}
}

// Enabled reports whether to look for updates.
func Enabled(cur Current, cfg config.Config) bool {
	return cur.Method != Dev && os.Getenv(DisableEnv) == "" && cfg.UpdateCheckEnabled()
}

// Notice is the message that tells the user about a newer version.
func (c Current) Notice(latest string) string {
	if c.Method == GoInstall {
		return "Rho update to version " + latest + " is available! Quit and run '" + GoInstallCmd + "' to update to the latest version."
	}
	return "Rho update to version " + latest + " is available! Quit and run 'rho update' to update to the latest version."
}

// Newer reports whether version a is newer than b. Versions are
// MAJOR.MINOR.PATCH with an optional "v" prefix and "-suffix"; a version
// with a suffix (a pre-release or Go pseudo-version) is older than the
// same version without one.
func Newer(a, b string) bool {
	ac, as := splitVersion(a)
	bc, bs := splitVersion(b)
	for i := range ac {
		if ac[i] != bc[i] {
			return ac[i] > bc[i]
		}
	}
	return as == "" && bs != ""
}

func splitVersion(v string) ([3]int, string) {
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "+")
	core, suffix, _ := strings.Cut(v, "-")
	var n [3]int
	for i, part := range strings.SplitN(core, ".", 3) {
		n[i], _ = strconv.Atoi(part)
	}
	return n, suffix
}
