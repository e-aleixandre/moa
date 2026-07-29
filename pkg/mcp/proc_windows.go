//go:build windows

package mcp

import "os/exec"

// setProcGroup is a no-op on Windows — POSIX process groups are not available.
func setProcGroup(cmd *exec.Cmd) {}

// killProcGroup is a no-op on Windows — see setProcGroup.
func killProcGroup(cmd *exec.Cmd) {}
