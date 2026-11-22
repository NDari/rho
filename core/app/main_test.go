package app

import (
	"fmt"
	"os"
	"testing"

	"github.com/NDari/rho/core/ext/yaegihost"
)

// TestMain lets the test binary run Go source extensions, as rho does with
// "rho ext-host <path>", since an App started by a test launches the
// running executable.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "ext-host" {
		if err := yaegihost.Main(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
