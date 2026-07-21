//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package attachment

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

type storeLock struct {
	file *os.File
}

func acquireStoreLock(path string) (io.Closer, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("attachment: open catalog lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("attachment: secure catalog lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("attachment: lock catalog: %w", err)
	}
	return &storeLock{file: file}, nil
}

func (l *storeLock) Close() error {
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
