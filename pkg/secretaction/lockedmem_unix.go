//go:build unix

package secretaction

import (
	"golang.org/x/sys/unix"
)

// lockedBuffer keeps a value in an anonymous private mapping outside the Go
// heap: the collector never copies it, mlock keeps it out of swap and, on
// Linux, it is excluded from core dumps.
type lockedBuffer struct {
	mem []byte
	n   int
}

func newLockedBuffer(value []byte) (*lockedBuffer, error) {
	page := unix.Getpagesize()
	size := (len(value) + page - 1) / page * page
	mem, err := unix.Mmap(-1, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return nil, err
	}
	if err := unix.Mlock(mem); err != nil {
		_ = unix.Munmap(mem)
		return nil, err
	}
	if err := excludeFromDumps(mem); err != nil {
		_ = unix.Munlock(mem)
		_ = unix.Munmap(mem)
		return nil, err
	}
	copy(mem, value)
	return &lockedBuffer{mem: mem, n: len(value)}, nil
}

func (b *lockedBuffer) bytes() []byte { return b.mem[:b.n] }

func (b *lockedBuffer) destroy() {
	clear(b.mem)
	_ = unix.Munlock(b.mem)
	_ = unix.Munmap(b.mem)
	b.mem, b.n = nil, 0
}
