//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package serve

import (
	"os"
	"syscall"
)

// lockVoiceJournal takes the journal's exclusive lock without waiting. The
// kernel releases it when its holder dies, which is what marks a call lost.
func lockVoiceJournal(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
