//go:build !windows

package tool

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// pollablePTY returns the terminal master as a file Go's poller manages, so
// write deadlines are real. creack/pty hands it back in blocking mode, where
// SetWriteDeadline is accepted and ignored. The descriptor is duplicated
// first: making the original non-blocking under a blocking *os.File would
// turn its reads into EAGAIN errors. f is closed either way.
func pollablePTY(f *os.File) (*os.File, error) {
	defer func() { _ = f.Close() }()
	rc, err := f.SyscallConn()
	if err != nil {
		return nil, err
	}
	nfd := -1
	var dupErr error
	// F_DUPFD_CLOEXEC: the copy is close-on-exec from birth, so a command
	// another session starts at that moment cannot inherit the terminal.
	if err := rc.Control(func(fd uintptr) { nfd, dupErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0) }); err != nil {
		return nil, err
	}
	if dupErr != nil {
		return nil, dupErr
	}
	if err := syscall.SetNonblock(nfd, true); err != nil {
		_ = syscall.Close(nfd)
		return nil, err
	}
	return os.NewFile(uintptr(nfd), f.Name()), nil
}
