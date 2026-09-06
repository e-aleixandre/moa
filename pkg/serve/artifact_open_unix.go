//go:build unix

package serve

import (
	"os"
	"syscall"
)

// openArtifactLeaf opens the final component relative to root without
// following it if it is a symlink (O_NOFOLLOW) and without blocking on a FIFO
// planted at that name (O_NONBLOCK) — a blocking open would hang the handler
// before the caller can reject a non-regular file. The caller still checks
// Mode().IsRegular() on the descriptor.
func openArtifactLeaf(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
