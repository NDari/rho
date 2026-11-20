package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Example is a starting configuration for a local OpenAI-compatible
// server such as llama.cpp's llama-server (port 9931 by default; older
// versions use 8080).
const Example = `default_model = "local"

[providers.local]
base_url = "http://localhost:9931/v1"

[[models]]
id = "local"
provider = "local"
context_window = 65536
`

// CreateExample writes Example to path, creating its directory. It never
// replaces an existing file.
func CreateExample(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists", path)
		}
		return err
	}
	if _, err := f.WriteString(Example); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}
