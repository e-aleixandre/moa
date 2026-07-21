//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package attachment

import (
	"errors"
	"io"
)

func acquireStoreLock(string) (io.Closer, error) {
	return nil, errors.New("attachment: catalog locking is unsupported on this platform")
}
