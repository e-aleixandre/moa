//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package serve

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

var errInstructionStoreInUse = errors.New("canonical instruction ledger is already in use")

func instructionStoreLockSupported() bool { return true }

type unixInstructionStoreLock struct {
	file *os.File
}

// acquireInstructionStoreLock owns the canonical replay/WAL ledger for the
// process lifetime. A second Serve must not load and later overwrite a stale
// snapshot of instruction outcomes.
func acquireInstructionStoreLock(path string) (io.Closer, error) {
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open canonical instruction ledger lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure canonical instruction ledger lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %v", errInstructionStoreInUse, err)
	}
	return &unixInstructionStoreLock{file: file}, nil
}

func (l *unixInstructionStoreLock) Close() error {
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
