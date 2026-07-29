//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

// setProcGroup puts the server subprocess in its own process group so the whole
// tree can be killed together. The MCP SDK's transport Close only signals the
// direct child (e.g. the node process), which leaves grandchildren — most
// painfully a Chrome spawned by a Playwright server — orphaned. Running in a
// dedicated group lets killProcGroup reap them all.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcGroup force-kills the entire process group. Safe to call even if the
// main process already exited (the kill just fails silently).
func killProcGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
