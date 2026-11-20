package kritrim

import (
	"context"
	"os/exec"
	"strings"
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

func TestDockerRunArgsHardened(t *testing.T) {
	args := strings.Join(dockerRunArgs(DockerConfig{}, "n", "img", OpenConfig{}, ""), " ")
	for _, want := range []string{"--cap-drop ALL", "--security-opt no-new-privileges", "--network none",
		"--memory 2g", "--cpus 2", "--pids-limit 512"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %s", want, args)
		}
	}
	args = strings.Join(dockerRunArgs(DockerConfig{User: "1000:1000", ReadOnly: true, Memory: "none",
		CPUs: "none", PidsLimit: -1, Network: true, CapAdd: []string{"CHOWN"}}, "n", "img", OpenConfig{}, ""), " ")
	for _, want := range []string{"--user 1000:1000", "--read-only", "--tmpfs /tmp", "--cap-add CHOWN"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %s", want, args)
		}
	}
	for _, bad := range []string{"--memory", "--cpus", "--pids-limit", "--network none"} {
		if strings.Contains(args, bad) {
			t.Fatalf("unexpected %q in %s", bad, args)
		}
	}
}

func TestDockerHardening(t *testing.T) {
	args := strings.Join(dockerRunArgs(DockerConfig{}, "n", "img", OpenConfig{}, ""), " ")
	for _, want := range []string{"--ulimit core=0", "--ipc private", "--cap-drop ALL", "no-new-privileges"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	if _, err := (DockerConfig{Seccomp: "unconfined"}).hardened(); err == nil {
		t.Error("unconfined seccomp accepted")
	}
	if _, err := (DockerConfig{Seccomp: "/no/such/profile.json"}).hardened(); err == nil {
		t.Error("missing seccomp profile accepted")
	}
	if _, err := (DockerConfig{Strict: true, CapAdd: []string{"NET_RAW"}}).hardened(); err == nil {
		t.Error("Strict accepted CapAdd")
	}
	if _, err := (DockerConfig{Strict: true, User: "0:0"}).hardened(); err == nil {
		t.Error("Strict accepted root")
	}
	c, err := (DockerConfig{Strict: true}).hardened()
	if err != nil || c.User != strictUser || !c.ReadOnly {
		t.Fatalf("Strict not applied: %+v %v", c, err)
	}
}

func TestDockerStrictSandbox(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m, err := Docker(DockerConfig{Image: "alpine:3.20", Strict: true}).Open(ctx, OpenConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(ctx)
	res, err := m.Exec(ctx, ExecRequest{Command: []string{"id", "-u"}, Timeout: 30 * time.Second})
	if err != nil || strings.TrimSpace(res.Stdout) != "65534" {
		t.Fatalf("not running as nobody: %+v %v", res, err)
	}
	if err := m.Write(ctx, "/tmp/f.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := m.Read(ctx, "/tmp/f.txt"); err != nil || string(b) != "hello" {
		t.Fatalf("%q %v", b, err)
	}
	res, _ = m.Exec(ctx, ExecRequest{Command: []string{"touch", "/etc/x"}, Timeout: 30 * time.Second})
	if res.Exit == 0 {
		t.Fatal("root filesystem is writable")
	}
	res, _ = m.Exec(ctx, ExecRequest{Command: []string{"sh", "-c", "ulimit -c"}, Timeout: 30 * time.Second})
	if strings.TrimSpace(res.Stdout) != "0" {
		t.Fatalf("core dumps enabled: %q", res.Stdout)
	}
}
