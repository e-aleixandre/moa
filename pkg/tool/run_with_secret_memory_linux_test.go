//go:build linux

package tool

import (
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/e-aleixandre/moa/pkg/secretaction"
)

// The pollable copy of the terminal is close-on-exec, so commands started
// elsewhere never inherit it.
func TestPollablePTYIsCloseOnExec(t *testing.T) {
	master, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tty.Close() }()
	f, err := pollablePTY(master)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rc, _ := f.SyscallConn()
	var flags int
	_ = rc.Control(func(fd uintptr) { flags, err = unix.FcntlInt(fd, unix.F_GETFD, 0) })
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("flags = %#x, err = %v", flags, err)
	}
}

// A terminal whose program never reads fills up. Typing a remembered value
// into it must give up in bounded time, leave nothing writing once it has
// given up (so nothing lands after Forget), and not hold Forget behind it.
func TestRememberedWriteIntoAStalledTerminalIsBounded(t *testing.T) {
	master, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tty.Close() }()
	raw := exec.Command("stty", "raw", "-echo")
	raw.Stdin = tty
	if err := raw.Run(); err != nil {
		t.Fatal(err)
	}
	f, err := pollablePTY(master)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	queued := func() int {
		n, err := unix.IoctlGetInt(int(tty.Fd()), unix.TIOCINQ)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	m := secretaction.NewMemory()
	defer m.Shutdown()
	gens, _, err := m.RememberMany([]secretaction.MemoryKey{vaultKey}, [][]byte{[]byte(memValue)}, time.Hour, m.Epoch(), secretaction.MemoryUse{})
	if err != nil {
		t.Fatal(err)
	}
	value := make([]byte, secretaction.MaxHoldValueBytes)
	for i := range value {
		value[i] = 'x'
	}
	const timeout = 300 * time.Millisecond
	stuck := false
	for i := 0; i < 400 && !stuck; i++ {
		start := time.Now()
		ran, werr := m.Dispatch(vaultKey, gens[0], func() error { return boundedWrite(f, value, timeout) })
		if !ran {
			t.Fatal("not dispatched")
		}
		if werr != nil {
			if !errors.Is(werr, errWriteStuck) {
				t.Fatalf("write error = %v", werr)
			}
			if d := time.Since(start); d > timeout+time.Second {
				t.Fatalf("stuck write took %v", d)
			}
			stuck = true
		}
	}
	if !stuck {
		t.Fatal("the terminal never filled")
	}
	done := make(chan struct{})
	go func() { m.ForgetAll(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Forget blocked behind a stuck write")
	}
	// Throw away everything the terminal holds. A write still in progress
	// would now get through; one that really stopped leaves it empty.
	if err := unix.IoctlSetInt(int(tty.Fd()), unix.TCFLSH, unix.TCIFLUSH); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := queued(); n != 0 {
		t.Fatalf("%d bytes arrived after the write gave up and Forget returned", n)
	}
}
