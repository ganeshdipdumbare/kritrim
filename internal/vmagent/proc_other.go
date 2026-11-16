//go:build !unix

package vmagent

import "os/exec"

// setProcessGroup does nothing where process groups are not available.
func setProcessGroup(cmd *exec.Cmd) {}

// killGroup kills the direct child only, the most that is available here.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
