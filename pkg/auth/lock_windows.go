//go:build windows

package auth

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func withPlatformFileLock(path string, fn func() error) error {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("opening credential lock: %w", err)
	}
	defer lock.Close() //nolint:errcheck
	var overlap windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(lock.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlap); err != nil {
		return fmt.Errorf("locking credentials: %w", err)
	}
	defer windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, &overlap) //nolint:errcheck
	return fn()
}

// Windows does not support opening a directory as a regular file; Rename is
// durable enough once the replacement file itself has been synced.
func syncDir(string) error { return nil }
