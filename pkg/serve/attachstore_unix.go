//go:build unix

package serve

import (
	"fmt"
	"os"
	"syscall"
)

// noFollowFlag adds O_NOFOLLOW so writes refuse to traverse a symlink placed at
// the target path. Unix-only; on other platforms it is zero.
const noFollowFlag = syscall.O_NOFOLLOW

// checkDirOwner rejects a directory owned by a different user, closing a local
// tampering vector. Uses Unix ownership (uid) semantics.
func checkDirOwner(path string, info os.FileInfo) error {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if int(st.Uid) != os.Getuid() {
			return fmt.Errorf("attachments base dir %q is not owned by the current user", path)
		}
	}
	return nil
}
