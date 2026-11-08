package kritrim

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestLocalSandbox(t *testing.T) {
	ctx := context.Background()
	m, err := Local().Open(ctx, OpenConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(ctx)
	res, err := m.Exec(ctx, ExecRequest{Command: []string{"echo", "hi"}, Timeout: 5 * time.Second})
	if err != nil || res.Stdout != "hi\n" || res.Exit != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if err := m.Write(ctx, t.TempDir()+"/f", []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDockerSandbox(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m, err := Docker(DockerConfig{Image: "alpine:3.20"}).Open(ctx, OpenConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(ctx)
	res, err := m.Exec(ctx, ExecRequest{Command: []string{"echo", "box"}, Timeout: 30 * time.Second})
	if err != nil || res.Stdout != "box\n" {
		t.Fatalf("%+v %v", res, err)
	}
	if err := m.Write(ctx, "/tmp/f.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := m.Read(ctx, "/tmp/f.txt")
	if err != nil || string(b) != "hello" {
		t.Fatalf("%q %v", b, err)
	}
}
