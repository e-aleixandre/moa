package secretaction

import "golang.org/x/sys/unix"

func excludeFromDumps(mem []byte) error {
	return unix.Madvise(mem, unix.MADV_DONTDUMP)
}
