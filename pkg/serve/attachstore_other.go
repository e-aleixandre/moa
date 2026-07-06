//go:build !unix

package serve

import "os"

// noFollowFlag is zero on platforms without O_NOFOLLOW (e.g. Windows).
const noFollowFlag = 0

// checkDirOwner is a no-op on platforms without Unix ownership semantics.
func checkDirOwner(path string, info os.FileInfo) error {
	return nil
}
