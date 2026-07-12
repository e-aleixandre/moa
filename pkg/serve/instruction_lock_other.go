//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package serve

import (
	"errors"
	"io"
)

var errInstructionStoreInUse = errors.New("canonical instruction ledger locking is unsupported on this platform")

func instructionStoreLockSupported() bool { return false }

// Fail closed where an advisory lifetime lock cannot be implemented safely.
func acquireInstructionStoreLock(string) (io.Closer, error) {
	return nil, errInstructionStoreInUse
}
