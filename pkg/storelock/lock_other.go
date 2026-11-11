//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package storelock

import (
	"os"
	"path/filepath"
)

// Lock is a no-op where flock is unavailable: the store keeps its previous,
// unserialized behavior there, and the dreaming refuses to run (see Supported).
func Lock(dir string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(Path(dir)), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(Path(dir), os.O_CREATE|os.O_RDWR, 0o600)
}

// TryLock always succeeds without excluding anyone.
func TryLock(dir string) (*os.File, bool, error) {
	f, err := Lock(dir)
	return f, err == nil, err
}

// Supported reports whether Lock really excludes other writers.
const Supported = false
