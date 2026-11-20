package update

import (
	"path/filepath"
	"strings"
)

// appBundle returns the .app bundle exe runs from, or "".
func appBundle(exe string) string {
	macos := filepath.Dir(exe)
	contents := filepath.Dir(macos)
	app := filepath.Dir(contents)
	if filepath.Base(macos) == "MacOS" && filepath.Base(contents) == "Contents" && strings.HasSuffix(app, ".app") {
		return app
	}
	return ""
}
