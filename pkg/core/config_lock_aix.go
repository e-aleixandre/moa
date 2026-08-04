//go:build aix

package core

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockConfigFile(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	lock := unix.Flock_t{Type: unix.F_WRLCK}
	if err := unix.FcntlFlock(file.Fd(), unix.F_SETLKW, &lock); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquiring advisory lock: %w", err)
	}
	return func() {
		unlock := unix.Flock_t{Type: unix.F_UNLCK}
		_ = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &unlock)
		_ = file.Close()
	}, nil
}
