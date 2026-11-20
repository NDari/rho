// Command rho is a minimal-core coding agent harness.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/update"
)

func main() {
	current = update.Detect(version)
	version = current.Version
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "rho:", err)
		if errors.Is(err, config.ErrNoModel) {
			fmt.Fprint(os.Stderr, noModelHint())
		}
		os.Exit(1)
	}
}
