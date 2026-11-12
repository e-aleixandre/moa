//go:build !unix

package secretaction

import "errors"

type lockedBuffer struct {
	mem []byte
	n   int
}

func newLockedBuffer([]byte) (*lockedBuffer, error) {
	return nil, errors.New("locked memory is not supported on this platform")
}

func (b *lockedBuffer) bytes() []byte { return b.mem[:b.n] }

func (b *lockedBuffer) destroy() {}
