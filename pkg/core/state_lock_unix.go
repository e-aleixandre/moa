//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package core

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

type stateLock struct {
	file *os.File
}

// acquireStateLock takes an exclusive advisory lock, so a read-modify-write of
// the project state is not lost when two moa processes update it at once —
// reachable with one user running two sessions in the same project.
func acquireStateLock(path string) (io.Closer, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("core: open project state lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("core: secure project state lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("core: lock project state: %w", err)
	}
	return &stateLock{file: file}, nil
}

func (l *stateLock) Close() error {
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
