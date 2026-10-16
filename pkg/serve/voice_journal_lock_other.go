//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package serve

import "os"

// lockVoiceJournal is a no-op where file locks are not supported: there a
// second server sharing the config can take a live call for a lost one.
func lockVoiceJournal(*os.File) error { return nil }
