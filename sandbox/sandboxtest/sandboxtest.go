// Package sandboxtest is the conformance suite every kritrim sandbox adapter
// must pass.
package sandboxtest

import (
	"context"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// Run checks exec, files, long-lived stdio and lifecycle on one sandbox,
// using /tmp inside the sandbox for scratch files.
func Run(t *testing.T, s kritrim.Sandbox) {
	t.Helper()
	RunDir(t, s, "/tmp")
}

// RunDir is Run with an explicit scratch directory inside the sandbox. Use
// t.TempDir() for sandboxes that share the host filesystem (Local).
func RunDir(t *testing.T, s kritrim.Sandbox, dir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	m, err := s.Open(ctx, kritrim.OpenConfig{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if err := m.Close(context.Background()); err != nil {
			t.Fatalf("close: %v", err)
		}
	}()

	res, err := m.Exec(ctx, kritrim.ExecRequest{Command: []string{"echo", "hi"}, Timeout: 30 * time.Second})
	if err != nil || strings.TrimSpace(res.Stdout) != "hi" || res.Exit != 0 {
		t.Fatalf("exec: %+v %v", res, err)
	}

	path := strings.TrimSuffix(dir, "/") + "/kritrim-conformance-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".txt"
	if err := m.Write(ctx, path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := m.Read(ctx, path)
	if err != nil || string(b) != "hello" {
		t.Fatalf("read: %q %v", b, err)
	}

	p, err := m.Start(ctx, kritrim.StartRequest{Command: []string{"sh", "-c", "echo ready"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	out, _ := io.ReadAll(p.Stdout())
	_ = p.Wait()
	if !strings.Contains(string(out), "ready") {
		t.Fatalf("start output %q", out)
	}

	// Close is idempotent.
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
