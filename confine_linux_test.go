//go:build linux && (amd64 || arm64)

package kritrim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// confineProbeEnv makes the test binary, run as a command inside a Confined
// sandbox, report what the kernel let it do instead of running tests.
const confineProbeEnv = "KRITRIM_CONFINE_PROBE"

func init() {
	arg, ok := os.LookupEnv(confineProbeEnv)
	if !ok {
		return
	}
	res := map[string]string{}
	errno := func(err error) string {
		if err == nil {
			return "ok"
		}
		var e syscall.Errno
		if errors.As(err, &e) {
			return e.Error()
		}
		return err.Error()
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err == nil {
		sa := &syscall.SockaddrInet4{Port: 9, Addr: [4]byte{127, 0, 0, 1}}
		err = syscall.Connect(fd, sa)
		if errors.Is(err, syscall.ECONNREFUSED) {
			err = nil // reached the stack: the network was not blocked
		}
		syscall.Close(fd)
	}
	res["inet"] = errno(err)
	ufd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.Close(ufd)
	}
	res["unix"] = errno(err)
	res["unshare"] = errno(syscall.Unshare(syscall.CLONE_NEWUSER))
	_, _, e := syscall.RawSyscall(syscall.SYS_PTRACE, syscall.PTRACE_TRACEME, 0, 0)
	if e != 0 {
		res["ptrace"] = e.Error()
	} else {
		res["ptrace"] = "ok"
	}
	_, err = os.ReadFile(arg)
	res["outside"] = errno(err)
	_, err = os.ReadFile(fmt.Sprintf("/proc/%d/environ", os.Getppid()))
	res["proc"] = errno(err)
	err = os.WriteFile(filepath.Join(os.Getenv("TMPDIR"), "w"), []byte("x"), 0o600)
	res["workspace"] = errno(err)
	_, err = os.ReadFile("/etc/passwd")
	res["etc"] = errno(err)
	res["signal"] = errno(syscall.Kill(os.Getppid(), 0))
	abi, _ := landlockABI()
	res["abi"] = fmt.Sprint(abi)
	out, _ := json.Marshal(res)
	os.Stdout.Write(out)
	os.Exit(0)
}

func TestConfinedEnforces(t *testing.T) {
	if err := confineSupported(false); err != nil {
		t.Skip(err)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	m, err := Confined(ConfinedConfig{}).Open(ctx, OpenConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ws := m.(*localMachine).workspace
	defer func() {
		_ = m.Close(ctx)
		if _, err := os.Stat(ws); !os.IsNotExist(err) {
			t.Error("workspace not removed on Close")
		}
	}()
	self, _ := os.Executable()
	res, err := m.Exec(ctx, ExecRequest{Command: []string{self}, Env: []string{confineProbeEnv + "=" + secret}})
	if err != nil || res.Exit != 0 {
		t.Fatalf("probe: %+v %v", res, err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(res.Stdout), &got); err != nil {
		t.Fatalf("probe output %q: %v", res.Stdout, err)
	}
	t.Logf("probe: %v", got)
	want := map[string]string{"unix": "ok", "workspace": "ok", "etc": "ok"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	deny := []string{"inet", "unshare", "ptrace", "outside", "proc"}
	if n, _ := strconv.Atoi(got["abi"]); n >= 6 {
		deny = append(deny, "signal")
	}
	for _, k := range deny {
		if got[k] == "ok" || got[k] == "" {
			t.Errorf("%s was allowed inside the sandbox", k)
		}
	}
}

func TestConfinedCommands(t *testing.T) {
	if err := confineSupported(false); err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	m, err := Confined(ConfinedConfig{}).Open(ctx, OpenConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(ctx)
	res, err := m.Exec(ctx, ExecRequest{Command: []string{"sh", "-c", "echo hi > f && cat f && ulimit -c"}})
	if err != nil || res.Exit != 0 || res.Stdout != "hi\n0\n" {
		t.Fatalf("%+v %v", res, err)
	}
	res, _ = m.Exec(ctx, ExecRequest{Command: []string{"sh", "-c", "echo x > /tmp/kritrim-escape"}})
	if res.Exit == 0 {
		_ = os.Remove("/tmp/kritrim-escape")
		t.Fatal("wrote outside the workspace")
	}
	res, _ = m.Exec(ctx, ExecRequest{Command: []string{"env"}})
	if strings.Contains(res.Stdout, confineEnv) {
		t.Fatal("confinement spec leaked into the command's environment")
	}
	// Streaming takes the same path.
	sm := m.(StreamingMachine)
	res, err = sm.ExecStream(ctx, ExecRequest{Command: []string{"cat", "/proc/1/environ"}}, nil)
	if err != nil || res.Exit == 0 {
		t.Fatalf("streamed command read /proc: %+v %v", res, err)
	}
}

func TestConfinedRejectsBadPaths(t *testing.T) {
	for _, p := range []string{"relative", "/", "/a/../b", "/no/such/dir/kritrim"} {
		if _, err := confinePath(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

// A sealed agent runs in a Confined sandbox: the box starts, talks to the
// host over stdio and finishes a run with a host-approved tool.
func TestSealedConfined(t *testing.T) {
	if err := confineSupported(false); err != nil {
		t.Skip(err)
	}
	if testing.Short() {
		t.Skip("short mode")
	}
	bin := filepath.Join(t.TempDir(), "box")
	build := exec.Command("go", "build", "-o", bin, "./internal/boxtest")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build box: %v: %s", err, out)
	}
	refund := Risky(NewTool("refund", "refund", func(ctx context.Context, in struct {
		Order string `json:"order"`
	}) (string, error) {
		return "refunded " + in.Order, nil
	}))
	a, err := New(Config{
		Model:   Mock(TurnTool("refund", map[string]any{"order": "A-17"}), Turn{Text: "done"}),
		Tools:   []Tool{refund},
		Approve: func(ctx context.Context, req Approval) (Decision, error) { return Approve(), nil },
		Sealed:  &Sealed{Sandbox: Confined(ConfinedConfig{}), Binary: bin},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	res, err := a.Run(context.Background(), "refund A-17")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" || res.Steps[0].ToolResults[0].Content != "refunded A-17" {
		t.Fatalf("res=%+v", res)
	}
}
