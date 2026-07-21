//go:build unix

package attachment

import (
	"os"
	"syscall"
)

func openBlobReadOnly(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
