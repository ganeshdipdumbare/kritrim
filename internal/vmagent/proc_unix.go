//go:build unix

package vmagent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group, so a kill
// reaches everything it started, not only the direct child.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the command's whole process group.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
