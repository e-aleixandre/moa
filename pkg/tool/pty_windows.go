//go:build windows

package tool

import (
	"errors"
	"os"
)

func pollablePTY(f *os.File) (*os.File, error) {
	_ = f.Close()
	return nil, errors.New("no terminal support on windows")
}
