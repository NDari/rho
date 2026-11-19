//go:build !windows

package tools

func lockedFileHint(err error) error { return err }
