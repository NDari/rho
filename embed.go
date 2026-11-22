// Package rho holds the documentation and examples built into the rho
// binary, so a model working in Rho can learn to extend it (see rho docs).
package rho

import "embed"

// Files holds the user documentation, the built-in skills and the
// examples, at their paths in the repository.
//
//go:embed docs/configuration.md docs/extensions.md docs/rho-spec.md docs/gui.md docs/builtin examples
var Files embed.FS
