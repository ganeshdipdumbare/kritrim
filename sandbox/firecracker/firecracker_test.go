package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/internal/vmagent"
	"github.com/ganeshdipdumbare/kritrim/sandbox/sandboxtest"
)

// agentSocket serves the guest agent on a Unix socket that speaks
// firecracker's vsock handshake, so the host side runs unchanged.
func agentSocket(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(shortTemp(t), "v.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				var line []byte
				b := make([]byte, 1)
				for {
					if _, err := c.Read(b); err != nil {
						c.Close()
						return
					}
					if b[0] == '\n' {
						break
					}
					line = append(line, b[0])
				}
				if string(line) != fmt.Sprintf("CONNECT %d", vmagent.Port) {
					c.Close()
					return
				}
				io.WriteString(c, "OK 1073741824\n")
				vmagent.Serve(c, os.Environ())
			}()
		}
	}()
	return sock
}

// shortTemp keeps Unix socket paths under the OS length limit.
func shortTemp(t *testing.T) string {
	dir, err := os.MkdirTemp("/tmp", "kfc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

type fixedSandbox struct{ sock string }

func (s fixedSandbox) Open(ctx context.Context, opts kritrim.OpenConfig) (kritrim.Machine, error) {
	m := &machine{env: opts.Env}
	m.dial = func(ctx context.Context) (net.Conn, error) { return dialVsock(ctx, s.sock, vmagent.Port) }
	return m, m.ping(ctx)
}

func TestAgentConformance(t *testing.T) {
	sandboxtest.RunWith(t, fixedSandbox{sock: agentSocket(t)}, sandboxtest.Options{Dir: t.TempDir()})
}

func TestBootSequence(t *testing.T) {
	dir := shortTemp(t)
	sock := filepath.Join(dir, "api.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var mu sync.Mutex
	var calls []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/boot-source" && body["kernel_image_path"] != "/k" {
			http.Error(w, "bad kernel", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	go srv.Serve(l)
	defer srv.Close()
	a := apiClient(sock)
	if err := a.put(context.Background(), "/boot-source", map[string]any{"kernel_image_path": "/k"}); err != nil {
		t.Fatal(err)
	}
	if err := a.put(context.Background(), "/boot-source", map[string]any{"kernel_image_path": "/x"}); err == nil || !strings.Contains(err.Error(), "bad kernel") {
		t.Fatalf("error not surfaced: %v", err)
	}
	if len(calls) != 2 || calls[0] != "PUT /boot-source" {
		t.Fatalf("calls %v", calls)
	}
}

func TestConfigChecks(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("missing kernel accepted")
	}
	box, _ := New(Config{Kernel: "/k", RootFS: "/r", Binary: "definitely-not-firecracker"})
	if _, err := box.Open(context.Background(), kritrim.OpenConfig{Network: true}); err == nil || !strings.Contains(err.Error(), "TapDevice") {
		t.Fatalf("network without tap: %v", err)
	}
	if _, err := box.Open(context.Background(), kritrim.OpenConfig{}); err == nil {
		t.Fatal("opened without firecracker")
	}
}

func TestFirecrackerLive(t *testing.T) {
	kernel, rootfs := os.Getenv("KRITRIM_FC_KERNEL"), os.Getenv("KRITRIM_FC_ROOTFS")
	if _, err := os.Stat("/dev/kvm"); err != nil || kernel == "" || rootfs == "" {
		t.Skip("needs /dev/kvm, KRITRIM_FC_KERNEL and KRITRIM_FC_ROOTFS")
	}
	box, err := New(Config{Kernel: kernel, RootFS: rootfs})
	if err != nil {
		t.Fatal(err)
	}
	sandboxtest.Run(t, box)
}
