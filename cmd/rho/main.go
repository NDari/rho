// Command rho is a minimal-core coding agent harness.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "rho:", err)
		os.Exit(1)
	}
}
