//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package serve

import (
	"errors"
	"io"
)

var errOperationStoreInUse = errors.New("Pulse operation store locking is unsupported on this platform")

func acquireOperationStoreLock(string) (io.Closer, error) {
	return nil, errOperationStoreInUse
}
