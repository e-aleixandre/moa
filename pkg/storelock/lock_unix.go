//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package storelock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock takes the exclusive lock of dir and returns the open lock file; closing
// it releases the lock. The file is returned (not an io.Closer) so a caller can
// hand the descriptor to a child process that must keep the lock held while it
// runs: a flock belongs to the open file description, not to the process.
//
// flock does not nest within a process either: a second Lock on the same store
// from the same goroutine blocks forever, so a holder writes the store directly.
func Lock(dir string) (*os.File, error) {
	path := Path(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("storelock: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("storelock: open %s: %w", path, err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("storelock: lock %s: %w", path, err)
	}
	return f, nil
}

// TryLock is Lock without waiting: ok is false when another holder has it.
func TryLock(dir string) (f *os.File, ok bool, err error) {
	path := Path(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, fmt.Errorf("storelock: %w", err)
	}
	f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("storelock: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("storelock: lock %s: %w", path, err)
	}
	return f, true, nil
}

// Supported reports whether Lock really excludes other writers.
const Supported = true
