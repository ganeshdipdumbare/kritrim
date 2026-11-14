package microsandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/sandbox/sandboxtest"
)

// TestMain lets the test binary stand in for the msb CLI: it understands
// the commands the adapter issues and runs them on the local machine.
func TestMain(m *testing.M) {
	if os.Getenv("KRITRIM_FAKE_MSB") != "" {
		os.Exit(fakeMSB(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func logCall(args []string) {
	if f, err := os.OpenFile(os.Getenv("KRITRIM_FAKE_MSB"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
}

func fakeMSB(args []string) int {
	logCall(args)
	switch args[0] {
	case "create", "rm":
		return 0
	case "copy":
		src, dst := args[1], args[2]
		strip := func(s string) string {
			if i := strings.Index(s, ":"); strings.HasPrefix(s, "kritrim-") && i > 0 {
				return s[i+1:]
			}
			return s
		}
		b, err := os.ReadFile(strip(src))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(strip(dst), b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case "exec":
		dir := ""
		i := 1
		for ; i < len(args) && args[i] != "--"; i++ {
			if args[i] == "-w" {
				dir = args[i+1]
				i++
			}
		}
		argv := args[i+1:]
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if strings.Contains(strings.Join(args[:i], " "), "--no-stdin") {
			cmd.Stdin = strings.NewReader("")
		}
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode()
			}
			fmt.Fprintln(os.Stderr, err)
			return 127
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "fake msb: unknown command", args[0])
	return 2
}

func TestMicrosandboxConformance(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("KRITRIM_FAKE_MSB", log)
	box, err := New(Config{Binary: os.Args[0], Image: "python", CPUs: 2, Memory: "1G"})
	if err != nil {
		t.Fatal(err)
	}
	sandboxtest.RunWith(t, box, sandboxtest.Options{Dir: t.TempDir()})
	calls, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if !strings.HasPrefix(lines[0], "create python --name kritrim-") || !strings.Contains(lines[0], "--no-net") ||
		!strings.Contains(lines[0], "-c 2 -m 1G") {
		t.Fatalf("create call: %s", lines[0])
	}
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "rm --force -q kritrim-") {
		t.Fatalf("last call: %s", last)
	}
	if !strings.Contains(string(calls), "exec -q --stream") {
		t.Fatalf("Start did not stream: %s", calls)
	}
}

func TestMicrosandboxMissingBinary(t *testing.T) {
	box, _ := New(Config{Binary: "definitely-not-msb"})
	if _, err := box.Open(context.Background(), kritrim.OpenConfig{}); err == nil {
		t.Fatal("opened without the CLI")
	}
}

func TestMicrosandboxLive(t *testing.T) {
	if _, err := exec.LookPath("msb"); err != nil || os.Getenv("KRITRIM_LIVE_MSB") == "" {
		t.Skip("set KRITRIM_LIVE_MSB with msb installed to run")
	}
	box, _ := New(Config{})
	sandboxtest.Run(t, box)
}
