//go:build windows

package tool

import "os/exec"

// setProcGroup is a no-op on Windows — process group management
// is not supported via the same POSIX mechanism.
func setProcGroup(cmd *exec.Cmd) {}

// killProcGroup is a no-op on Windows — see setProcGroup.
func killProcGroup(cmd *exec.Cmd) {}
