//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package serve

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

var errOperationStoreInUse = errors.New("Pulse operation store is already in use")

type unixOperationStoreLock struct {
	file *os.File
}

func acquireOperationStoreLock(path string) (io.Closer, error) {
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Pulse operation store lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure Pulse operation store lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %v", errOperationStoreInUse, err)
	}
	return &unixOperationStoreLock{file: file}, nil
}

func (l *unixOperationStoreLock) Close() error {
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
