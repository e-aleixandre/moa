//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package serve

import (
	"errors"
	"io"
)

var errDeviceStoreInUse = errors.New("device store locking is unsupported on this platform")

func deviceStoreLockSupported() bool { return false }

func acquireDeviceStoreLock(string) (io.Closer, error) {
	return nil, errDeviceStoreInUse
}
