// Package sandboxtest is the conformance suite every kritrim sandbox adapter
// must pass.
package sandboxtest

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// Run checks exec, files, long-lived stdio and lifecycle on one sandbox.
func Run(t *testing.T, s kritrim.Sandbox) {
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

	path := "/tmp/kritrim-conformance.txt"
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
