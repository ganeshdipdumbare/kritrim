package vmagent

import (
	"strings"
	"testing"
	"time"
)

// A file that never ends (or is a device) is refused, not read into memory.
func TestReadRefusesDevicesAndHugeFiles(t *testing.T) {
	if _, err := readFileCapped("/dev/zero"); err == nil {
		t.Fatal("read of /dev/zero succeeded")
	}
}

// Output past the cap is cut with a marker, and the command still returns.
func TestExecCapsOutput(t *testing.T) {
	r := execute(Request{Argv: []string{"sh", "-c", "head -c 9000000 /dev/zero | tr '\\0' a"}}, []string{"PATH=/usr/bin:/bin"})
	if r.Error != "" {
		t.Fatal(r.Error)
	}
	if !strings.Contains(string(r.Stdout), "[output truncated]") || len(r.Stdout) > maxReply+64 {
		t.Fatalf("stdout not capped: %d bytes", len(r.Stdout))
	}
}

// A timeout ends the command's children too. Before, a grandchild held the
// output pipe open and the command never returned.
func TestTimeoutKillsProcessTree(t *testing.T) {
	start := time.Now()
	execute(Request{Argv: []string{"sh", "-c", "sleep 30 & sleep 30"}, Timeout: 300}, []string{"PATH=/usr/bin:/bin"})
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("timed-out command took %v; its children kept the pipes open", d)
	}
}
