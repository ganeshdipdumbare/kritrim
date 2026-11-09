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

func TestLocalExecStream(t *testing.T) {
	ctx := context.Background()
	m, err := Local().Open(ctx, OpenConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(ctx)
	sm, ok := m.(StreamingMachine)
	if !ok {
		t.Fatal("Local does not implement StreamingMachine")
	}
	var chunks []string
	res, err := sm.ExecStream(ctx, ExecRequest{Command: []string{"sh", "-c", "echo one; echo two"}, Timeout: 10 * time.Second},
		func(stdout, stderr string) {
			if stdout != "" {
				chunks = append(chunks, stdout)
			}
		})
	if err != nil || res.Exit != 0 || len(chunks) != 2 || res.Stdout != "one\ntwo\n" {
		t.Fatalf("%+v chunks=%v err=%v", res, chunks, err)
	}
}

func TestDockerAttach(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not running")
	}
	name := "kritrim-attach-" + time.Now().Format("150405.000000")
	if out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "alpine:3.20", "sleep", "120").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v %s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", name).Run()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	m, err := Docker(DockerConfig{Container: name}).Open(ctx, OpenConfig{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Exec(ctx, ExecRequest{Command: []string{"echo", "attached"}, Timeout: 10 * time.Second})
	if err != nil || res.Stdout != "attached\n" {
		t.Fatalf("%+v %v", res, err)
	}
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("docker", "inspect", name).Run(); err != nil {
		t.Fatal("attached container was removed on Close")
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
