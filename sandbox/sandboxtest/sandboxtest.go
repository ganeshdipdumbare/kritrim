// Package sandboxtest is the conformance suite every kritrim sandbox adapter
// must pass.
package sandboxtest

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// Options configures RunWith.
type Options struct {
	// Dir is a scratch directory inside the sandbox. Default /tmp; use
	// t.TempDir() for sandboxes that share the host filesystem (Local).
	Dir string
	// NoStart is for providers that cannot stream a process's stdin and
	// stdout. Start must then fail with errors.ErrUnsupported, and the
	// sandbox cannot host a sealed agent.
	NoStart bool
}

// Run checks exec, files, long-lived stdio and lifecycle on one sandbox,
// using /tmp inside the sandbox for scratch files.
func Run(t *testing.T, s kritrim.Sandbox) {
	t.Helper()
	RunWith(t, s, Options{})
}

// RunDir is Run with an explicit scratch directory inside the sandbox.
func RunDir(t *testing.T, s kritrim.Sandbox, dir string) {
	t.Helper()
	RunWith(t, s, Options{Dir: dir})
}

// RunWith is Run with options.
func RunWith(t *testing.T, s kritrim.Sandbox, opts Options) {
	t.Helper()
	if opts.Dir == "" {
		opts.Dir = "/tmp"
	}
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
	res, err = m.Exec(ctx, kritrim.ExecRequest{Command: []string{"sh", "-c", "exit 3"}, Timeout: 30 * time.Second})
	if err != nil || res.Exit != 3 {
		t.Fatalf("exit code: %+v %v", res, err)
	}
	res, err = m.Exec(ctx, kritrim.ExecRequest{Command: []string{"cat"}, Stdin: []byte("from stdin"), Timeout: 30 * time.Second})
	if err != nil || strings.TrimSpace(res.Stdout) != "from stdin" {
		t.Fatalf("exec stdin: %+v %v", res, err)
	}

	path := strings.TrimSuffix(opts.Dir, "/") + "/kritrim-conformance-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".txt"
	if err := m.Write(ctx, path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := m.Read(ctx, path)
	if err != nil || string(b) != "hello" {
		t.Fatalf("read: %q %v", b, err)
	}
	script := strings.TrimSuffix(path, ".txt") + ".sh"
	if err := m.Write(ctx, script, []byte("#!/bin/sh\necho executable\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	res, err = m.Exec(ctx, kritrim.ExecRequest{Command: []string{script}, Timeout: 30 * time.Second})
	if err != nil || strings.TrimSpace(res.Stdout) != "executable" {
		t.Fatalf("mode 0755 not executable: %+v %v", res, err)
	}

	// The start context ends as soon as Start returns; the process must
	// keep running, as a sealed agent's does.
	startCtx, startCancel := context.WithCancel(ctx)
	p, err := m.Start(startCtx, kritrim.StartRequest{Command: []string{"sh", "-c", "read line; echo got $line"}})
	startCancel()
	if opts.NoStart {
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("start on a NoStart sandbox: %v, want errors.ErrUnsupported", err)
		}
	} else {
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if _, err := io.WriteString(p.Stdin(), "ready\n"); err != nil {
			t.Fatalf("start stdin: %v", err)
		}
		_ = p.Stdin().Close()
		out, _ := io.ReadAll(p.Stdout())
		_ = p.Wait()
		if !strings.Contains(string(out), "got ready") {
			t.Fatalf("start output %q", out)
		}
	}

	// Close is idempotent.
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
