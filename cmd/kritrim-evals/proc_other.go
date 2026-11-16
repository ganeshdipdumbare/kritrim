//go:build !unix

package main

import "os/exec"

// setProcessGroup does nothing where process groups are not available.
func setProcessGroup(cmd *exec.Cmd) {}

// killGroup kills the direct child, the most that is available here.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
