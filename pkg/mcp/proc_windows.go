//go:build windows

package mcp

import "os/exec"

// procGroupSupported is false on Windows: POSIX process groups are unavailable,
// so a restart's teardown cannot guarantee the old process tree (e.g. a
// Playwright-spawned browser) is reaped. RestartServer refuses on Windows
// rather than orphan processes; a full session restart still recreates servers.
const procGroupSupported = false

// setProcGroup is a no-op on Windows — POSIX process groups are not available.
func setProcGroup(cmd *exec.Cmd) {}

// killProcGroup is a no-op on Windows — see setProcGroup.
func killProcGroup(cmd *exec.Cmd) {}
