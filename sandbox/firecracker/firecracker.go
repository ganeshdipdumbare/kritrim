// Package firecracker provides a kritrim sandbox on Firecracker microVMs,
// on a Linux host with KVM.
//
//	box, err := firecracker.New(firecracker.Config{
//		Kernel: "/var/lib/kritrim/vmlinux",
//		RootFS: "/var/lib/kritrim/rootfs.ext4", // contains /usr/local/bin/kritrim-vmagent
//	})
//
// Each Open copies the root filesystem, starts a firecracker process,
// configures the VM through its API socket and boots it with
// kritrim-vmagent as init. Commands, files and processes go to the agent
// over vsock, so a microVM can host a sealed agent. Close kills the VM and
// deletes its files. The VM has no network device unless Network is
// requested and a TAP device is configured. For production, run
// firecracker under its jailer (Config.Binary may point at a wrapper).
package firecracker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/internal/boxutil"
	"github.com/ganeshdipdumbare/kritrim/internal/vmagent"
)

// Config configures Firecracker microVMs.
type Config struct {
	// Binary is the firecracker executable. Default "firecracker" on PATH.
	Binary string
	// Kernel is an uncompressed Linux kernel (vmlinux). Required.
	Kernel string
	// RootFS is an ext4 image with /usr/local/bin/kritrim-vmagent.
	// Required. Each VM boots a private copy, so it starts fresh.
	RootFS string
	// VCPUs and MemoryMiB size the VM. Default 1 and 512.
	VCPUs     int
	MemoryMiB int
	// TapDevice attaches a network interface when OpenConfig.Network is
	// set. Without it a networked Open is refused.
	TapDevice string
	// BootArgs overrides the kernel command line.
	BootArgs string
	// WorkDir holds each VM's sockets and disk copy. Default os.TempDir().
	WorkDir string
	// BootTimeout bounds how long Open waits for the agent. Default 30s.
	BootTimeout time.Duration
}

const defaultBootArgs = "console=ttyS0 reboot=k panic=1 pci=off init=/usr/local/bin/kritrim-vmagent"

// New validates the configuration.
func New(cfg Config) (kritrim.Sandbox, error) {
	if cfg.Kernel == "" || cfg.RootFS == "" {
		return nil, errors.New("firecracker: Kernel and RootFS are required")
	}
	if cfg.Binary == "" {
		cfg.Binary = "firecracker"
	}
	if cfg.VCPUs <= 0 {
		cfg.VCPUs = 1
	}
	if cfg.MemoryMiB <= 0 {
		cfg.MemoryMiB = 512
	}
	if cfg.BootArgs == "" {
		cfg.BootArgs = defaultBootArgs
	}
	if cfg.BootTimeout <= 0 {
		cfg.BootTimeout = 30 * time.Second
	}
	return &sandbox{cfg: cfg}, nil
}

type sandbox struct{ cfg Config }

func (s *sandbox) Open(ctx context.Context, opts kritrim.OpenConfig) (kritrim.Machine, error) {
	if opts.Network && s.cfg.TapDevice == "" {
		return nil, errors.New("firecracker: OpenConfig.Network needs Config.TapDevice")
	}
	bin, err := exec.LookPath(s.cfg.Binary)
	if err != nil {
		return nil, fmt.Errorf("firecracker: %s not found: %w", s.cfg.Binary, err)
	}
	dir, err := os.MkdirTemp(s.cfg.WorkDir, "kritrim-fc-")
	if err != nil {
		return nil, err
	}
	m := &machine{dir: dir, env: opts.Env}
	fail := func(err error) (kritrim.Machine, error) {
		_ = m.Close(context.Background())
		return nil, err
	}
	rootfs := filepath.Join(dir, "rootfs.ext4")
	if err := copyFile(s.cfg.RootFS, rootfs); err != nil {
		return fail(fmt.Errorf("firecracker: copy rootfs: %w", err))
	}
	apiSock := filepath.Join(dir, "api.sock")
	vsock := filepath.Join(dir, "vsock.sock")
	m.cmd = exec.Command(bin, "--api-sock", apiSock)
	m.cmd.Stdout, m.cmd.Stderr = io.Discard, io.Discard
	if err := m.cmd.Start(); err != nil {
		return fail(fmt.Errorf("firecracker: start: %w", err))
	}
	api := apiClient(apiSock)
	if err := waitFor(ctx, s.cfg.BootTimeout, func() error { _, err := os.Stat(apiSock); return err }); err != nil {
		return fail(fmt.Errorf("firecracker: api socket: %w", err))
	}
	steps := []struct {
		path string
		body any
	}{
		{"/machine-config", map[string]any{"vcpu_count": s.cfg.VCPUs, "mem_size_mib": s.cfg.MemoryMiB}},
		{"/boot-source", map[string]any{"kernel_image_path": s.cfg.Kernel, "boot_args": s.cfg.BootArgs}},
		{"/drives/rootfs", map[string]any{"drive_id": "rootfs", "path_on_host": rootfs, "is_root_device": true, "is_read_only": false}},
		{"/vsock", map[string]any{"guest_cid": 3, "uds_path": vsock}},
	}
	if opts.Network {
		steps = append(steps, struct {
			path string
			body any
		}{"/network-interfaces/eth0", map[string]any{"iface_id": "eth0", "host_dev_name": s.cfg.TapDevice}})
	}
	steps = append(steps, struct {
		path string
		body any
	}{"/actions", map[string]any{"action_type": "InstanceStart"}})
	for _, st := range steps {
		if err := api.put(ctx, st.path, st.body); err != nil {
			return fail(fmt.Errorf("firecracker: PUT %s: %w", st.path, err))
		}
	}
	m.dial = func(ctx context.Context) (net.Conn, error) { return dialVsock(ctx, vsock, vmagent.Port) }
	if err := waitFor(ctx, s.cfg.BootTimeout, func() error { return m.ping(ctx) }); err != nil {
		return fail(fmt.Errorf("firecracker: agent did not answer: %w", err))
	}
	return m, nil
}

// api talks to the firecracker API socket.
type api struct{ c *http.Client }

func apiClient(sock string) api {
	return api{c: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}}
}

func (a api) put(ctx context.Context, path string, body any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://firecracker"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// dialVsock connects to a guest port through firecracker's vsock Unix
// socket: "CONNECT <port>\n", answered by "OK <host port>\n".
func dialVsock(ctx context.Context, sock string, port int) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if _, err := fmt.Fprintf(c, "CONNECT %d\n", port); err != nil {
		c.Close()
		return nil, err
	}
	// Read the reply byte by byte so no stream data is buffered away.
	var line []byte
	b := make([]byte, 1)
	for len(line) < 64 {
		if _, err := c.Read(b); err != nil {
			c.Close()
			return nil, err
		}
		if b[0] == '\n' {
			break
		}
		line = append(line, b[0])
	}
	if !strings.HasPrefix(string(line), "OK ") {
		c.Close()
		return nil, fmt.Errorf("vsock handshake: %q", line)
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

func waitFor(ctx context.Context, limit time.Duration, try func() error) error {
	deadline := time.Now().Add(limit)
	for {
		err := try()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type machine struct {
	dir  string
	cmd  *exec.Cmd
	env  []string
	dial func(ctx context.Context) (net.Conn, error)

	mu     sync.Mutex
	closed bool
}

func (m *machine) call(ctx context.Context, req vmagent.Request) (vmagent.Reply, error) {
	c, err := m.dial(ctx)
	if err != nil {
		return vmagent.Reply{}, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	return vmagent.Call(c, req)
}

func (m *machine) ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := m.call(ctx, vmagent.Request{Op: "ping"})
	return err
}

func (m *machine) Exec(ctx context.Context, r kritrim.ExecRequest) (kritrim.ExecResult, error) {
	if len(r.Command) == 0 {
		return kritrim.ExecResult{}, errors.New("firecracker: empty command")
	}
	reply, err := m.call(ctx, vmagent.Request{Op: "exec", Argv: r.Command, Dir: r.Dir,
		Env: append(append([]string(nil), m.env...), r.Env...), Stdin: r.Stdin, HasIn: r.Stdin != nil,
		Timeout: r.Timeout.Milliseconds()})
	return kritrim.ExecResult{Stdout: string(reply.Stdout), Stderr: string(reply.Stderr), Exit: reply.Exit}, err
}

func (m *machine) Start(ctx context.Context, r kritrim.StartRequest) (kritrim.Process, error) {
	if len(r.Command) == 0 {
		return nil, errors.New("firecracker: empty command")
	}
	c, err := m.dial(ctx)
	if err != nil {
		return nil, err
	}
	req, _ := json.Marshal(vmagent.Request{Op: "start", Argv: r.Command, Dir: r.Dir,
		Env: append(append([]string(nil), m.env...), r.Env...)})
	if _, err := c.Write(append(req, '\n')); err != nil {
		c.Close()
		return nil, err
	}
	var wmu sync.Mutex
	send := func(typ byte, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return vmagent.WriteFrame(c, typ, b)
	}
	p := boxutil.NewProcess(
		func(b []byte) error { return send(vmagent.FrameStdin, b) },
		func() error { return send(vmagent.FrameClose, nil) },
		func() error { return send(vmagent.FrameKill, nil) },
	)
	// The connection, and so the process, outlives ctx: it ends when the
	// process exits, on Kill, or when the VM is closed.
	go func() {
		defer c.Close()
		br := bufio.NewReaderSize(c, 64*1024)
		for {
			typ, b, err := vmagent.ReadFrame(br)
			if err != nil {
				p.Exit(fmt.Errorf("firecracker: process stream: %w", err))
				return
			}
			switch typ {
			case vmagent.FrameStdout:
				_ = p.Output(b)
			case vmagent.FrameStderr:
				_, _ = os.Stderr.Write(b)
			case vmagent.FrameStartErr:
				p.Exit(fmt.Errorf("firecracker: start: %s", b))
				return
			case vmagent.FrameExit:
				code := 0
				if len(b) == 4 {
					code = int(int32(binary.BigEndian.Uint32(b)))
				}
				if code != 0 {
					p.Exit(&boxutil.ExitError{Code: code})
				} else {
					p.Exit(nil)
				}
				return
			}
		}
	}()
	return p, nil
}

func (m *machine) Read(ctx context.Context, path string) ([]byte, error) {
	reply, err := m.call(ctx, vmagent.Request{Op: "read", Path: path})
	if err != nil {
		return nil, fmt.Errorf("firecracker: read %s: %w", path, err)
	}
	return reply.Data, nil
}

func (m *machine) Write(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	if _, err := m.call(ctx, vmagent.Request{Op: "write", Path: path, Data: data, Mode: uint32(mode.Perm())}); err != nil {
		return fmt.Errorf("firecracker: write %s: %w", path, err)
	}
	return nil
}

func (m *machine) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		_ = m.cmd.Wait()
	}
	if m.dir != "" {
		return os.RemoveAll(m.dir)
	}
	return nil
}
