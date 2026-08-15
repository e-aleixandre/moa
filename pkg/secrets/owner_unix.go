//go:build unix

package secrets

import (
	"fmt"
	"os"
	"syscall"
)

func checkBaseOwner(path string, info os.FileInfo) error {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("secret base dir %q is not owned by the current user", path)
	}
	return nil
}
