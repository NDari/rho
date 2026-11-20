// Package update looks for new Rho releases on GitHub and installs them
// in place of the running copy.
package update

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/NDari/rho/core/config"
)

// Product describes a program released on GitHub that a Checker
// updates.
type Product struct {
	// Repo is the GitHub repository the releases come from.
	Repo string
	// Archive starts the release archive names, as in
	// rho_0.4.0_linux_amd64.tar.gz.
	Archive string
	// Programs are the programs a release archive holds. Install replaces
	// the running one and those of the others that are next to it.
	Programs []string
	// App starts the name of the zip file holding the macOS app, as in
	// Rho_0.4.0_macos.zip, or is "" when there is no app.
	App string
	// State is the file in the Rho home directory that keeps the last
	// check's result.
	State string
}

// CLI is the rho program.
var CLI = Product{Repo: "NDari/rho", Archive: "rho", Programs: []string{"rho"}, State: "update.json"}

// ArchiveName is the product's release archive for this platform.
func (p Product) ArchiveName(version string) string {
	return fmt.Sprintf("%s_%s_%s_%s%s", p.Archive, version, runtime.GOOS, runtime.GOARCH, archiveExt)
}

// AppName is the release file holding the product's macOS app.
func (p Product) AppName(version string) string {
	return fmt.Sprintf("%s_%s_macos.zip", p.App, version)
}

// GoInstallCmd updates a copy installed with go install.
const GoInstallCmd = "go install github.com/NDari/rho/cmd/rho@latest"

// DisableEnv turns update checks off when set to any value.
const DisableEnv = "RHO_NO_UPDATE_CHECK"

// Method says how the running program was installed.
type Method int

const (
	// Dev is a build from source without a version: no update checks.
	Dev Method = iota
	// Archive is a release archive or macOS app, which Install replaces.
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
