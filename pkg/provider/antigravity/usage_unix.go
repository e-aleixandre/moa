//go:build unix

package antigravity

import (
	"os/exec"
	"syscall"
)

// killGroupOnCancel starts cmd in its own process group and kills the whole
// group when its context ends.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
