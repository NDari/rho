//go:build !windows

package tools

func platformPath(p string) string { return p }
