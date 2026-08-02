// Package moa embeds the user-facing documentation into the binary.
//
// It lives at the repository root because go:embed cannot reach outside the
// directory of the file that declares it, and docs/ is a root directory. The
// package exposes nothing but the filesystem; pkg/moadocs does the reading.
package moa

import "embed"

// Docs holds docs/*.md as published on letmoa.run/docs. Only markdown is
// embedded: the assets are screenshots, useless to a language model and heavy
// enough (3.9 MB against 93 KB of text) to matter in the binary.
//
//go:embed docs/*.md docs/recipes/*.md
var Docs embed.FS
