//go:build !darwin

package update

// appBundle returns "": app bundles exist only on macOS.
func appBundle(string) string { return "" }
