//go:build windows

package antigravity

import "os/exec"

// killGroupOnCancel has no process group to kill on Windows: the context kills
// agy itself, and WaitDelay bounds the wait for anything holding its output.
func killGroupOnCancel(*exec.Cmd) {}
