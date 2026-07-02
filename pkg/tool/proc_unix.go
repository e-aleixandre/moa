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

// killProcGroup force-kills the entire process group (SIGKILL), reaping any
// grandchildren that ignored the SIGTERM sent on cancel. WaitDelay's own
// force-kill only targets the main process, so a setsid grandchild survives it.
// Safe to call after the main process has already exited.
func killProcGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
