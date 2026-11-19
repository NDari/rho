//go:build windows

package tools

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// lockedFileHint explains sharing violations, which are common on Windows
// when an editor or another process holds the file open.
func lockedFileHint(err error) error {
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return fmt.Errorf("%w (the file may be open or locked by another program)", err)
	}
	return err
}
