//go:build !windows

package tool

import (
	"os/exec"
	"syscall"
)

// setProcGroup configures the command to run in its own process group
// so we can kill the entire process tree on cancel.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
