//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package core

import "io"

// nopCloser stands in where advisory locking is unavailable. Updates stay
// atomic through the rename; only the read-modify-write is unprotected, which
// is the pre-existing behaviour on these platforms rather than a new risk.
type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func acquireStateLock(string) (io.Closer, error) { return nopCloser{}, nil }
