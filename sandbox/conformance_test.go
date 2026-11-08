package sandbox_test

import (
	"os/exec"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/sandbox/sandboxtest"
)

func TestLocalConformance(t *testing.T) { sandboxtest.Run(t, kritrim.Local()) }

func TestDockerConformance(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not running")
	}
	sandboxtest.Run(t, kritrim.Docker(kritrim.DockerConfig{Image: "alpine:3.20"}))
}
