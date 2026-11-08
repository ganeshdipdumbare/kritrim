package kritrim

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEnv(t *testing.T) context.Context {
	t.Helper()
	a := newAgent(t, Config{Model: Mock()})
	return context.WithValue(context.Background(), keyEnv, a.env)
}

func TestTimeTool(t *testing.T) {
	got, err := Time().run(context.Background(), nil)
	if err != nil || len(got) < 10 {
		t.Fatalf("%q %v", got, err)
	}
}

func TestShellAllowlist(t *testing.T) {
	ctx := testEnv(t)
	out, err := Shell(ShellConfig{Allow: []string{"echo"}}).run(ctx, map[string]any{"argv": []any{"echo", "hi"}})
	if err != nil || out != "hi" {
		t.Fatalf("%q %v", out, err)
	}
	_, err = Shell(ShellConfig{Allow: []string{"echo"}}).run(ctx, map[string]any{"argv": []any{"ls"}})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("want allowlist error, got %v", err)
	}
	if _, err := New(Config{Model: Mock(), Tools: []Tool{Shell(ShellConfig{})}}); err == nil {
		t.Fatal("want error for Shell with no allowlist")
	}
}

func TestFilesRootAndWrite(t *testing.T) {
	root := t.TempDir()
	ctx := testEnv(t)
	tool := Files(FilesConfig{Root: root, Write: true})
	if _, err := tool.run(ctx, map[string]any{"op": "write", "path": "a/b.txt", "content": "hello"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "a", "b.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("%q %v", b, err)
	}
	out, err := tool.run(ctx, map[string]any{"op": "read", "path": "a/b.txt"})
	if err != nil || out != "hello" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := tool.run(ctx, map[string]any{"op": "read", "path": "../escape"}); err == nil {
		t.Fatal("path escape allowed")
	}
	ro := Files(FilesConfig{Root: root})
	if _, err := ro.run(ctx, map[string]any{"op": "write", "path": "x", "content": "y"}); err == nil {
		t.Fatal("read-only write allowed")
	}
}

func TestHTTPAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong"))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	host = strings.Split(host, ":")[0]
	ctx := testEnv(t)
	tool := HTTP(HTTPConfig{AllowHosts: []string{host}})
	out, err := tool.run(ctx, map[string]any{"url": srv.URL})
	if err != nil || !strings.Contains(out, "pong") {
		t.Fatalf("%q %v", out, err)
	}
	bad := HTTP(HTTPConfig{AllowHosts: []string{"example.com"}})
	if _, err := bad.run(ctx, map[string]any{"url": srv.URL}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("want deny, got %v", err)
	}
	if _, err := New(Config{Model: Mock(), Tools: []Tool{HTTP(HTTPConfig{})}}); err == nil {
		t.Fatal("want error for HTTP with no allowlist")
	}
}

func TestHTTPRedirectBlocked(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secret"))
	}))
	defer target.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	targetURL := "http://localhost:" + port
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetURL, http.StatusFound)
	}))
	defer redirect.Close()
	hostOf := func(s string) string { return strings.Split(strings.TrimPrefix(s, "http://"), ":")[0] }
	ctx := testEnv(t)
	tool := HTTP(HTTPConfig{AllowHosts: []string{hostOf(redirect.URL)}})
	if _, err := tool.run(ctx, map[string]any{"url": redirect.URL}); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("want redirect denial, got %v", err)
	}
	both := HTTP(HTTPConfig{AllowHosts: []string{hostOf(redirect.URL), "localhost"}})
	if out, err := both.run(ctx, map[string]any{"url": redirect.URL}); err != nil || !strings.Contains(out, "secret") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestSecretResolution(t *testing.T) {
	t.Setenv("MY_TEST_SECRET", "super-secret-value")
	a := newAgent(t, Config{Model: Mock()})
	ctx := context.WithValue(context.Background(), keyEnv, a.env)
	v, err := a.env.Secret(ctx, "MY_TEST_SECRET")
	if err != nil || v != "super-secret-value" {
		t.Fatalf("%q %v", v, err)
	}
	if got := a.redactor.redact("x super-secret-value y"); got != "x [redacted] y" {
		t.Fatalf("redaction %q", got)
	}
}
