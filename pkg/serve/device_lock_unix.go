//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package serve

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

var errDeviceStoreInUse = errors.New("device store is already in use")

func deviceStoreLockSupported() bool { return true }

type unixDeviceStoreLock struct {
	file *os.File
}

func acquireDeviceStoreLock(path string) (io.Closer, error) {
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open device store lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure device store lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %v", errDeviceStoreInUse, err)
	}
	return &unixDeviceStoreLock{file: file}, nil
}

func (l *unixDeviceStoreLock) Close() error {
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
