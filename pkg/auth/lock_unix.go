//go:build !windows

package auth

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func withPlatformFileLock(path string, fn func() error) error {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("opening credential lock: %w", err)
	}
	defer lock.Close() //nolint:errcheck
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("locking credentials: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck
	return fn()
}

// tryLockFile takes an exclusive lock on f without blocking; false means
// another holder has it.
func tryLockFile(f *os.File) (bool, error) {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, unix.EWOULDBLOCK):
			return false, nil
		case !errors.Is(err, unix.EINTR):
			return false, err
		}
	}
}

func unlockFile(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close() //nolint:errcheck
	return dir.Sync()
}
