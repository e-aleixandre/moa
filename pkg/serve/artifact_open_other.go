//go:build !unix

package serve

import (
	"errors"
	"os"
)

// openArtifactLeaf is the portable fallback: platforms without O_NOFOLLOW get
// an Lstat check before the open and a SameFile check after it, so a leaf
// replaced in between is rejected rather than served.
func openArtifactLeaf(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("artifact leaf is not a regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, errors.New("artifact leaf changed while opening")
	}
	return f, nil
}
