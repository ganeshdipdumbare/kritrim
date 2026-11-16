// Package microsandbox provides a kritrim sandbox on microsandbox
// (microsandbox.dev) microVMs, through its msb CLI.
//
//	box, err := microsandbox.New(microsandbox.Config{Image: "alpine"})
//
// Each Open creates a microVM with `msb create` and Close removes it with
// `msb rm --force`. Commands run with `msb exec`, files move with
// `msb copy`, and Start streams a process's stdin and stdout, so a
// microsandbox VM can host a sealed agent. The network is off (--no-net)
// unless OpenConfig.Network is set. The msb CLI must be installed and its
// backend (local or cloud) configured.
package microsandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/internal/boxutil"
)

// Config configures microsandbox VMs.
type Config struct {
	// Binary is the msb CLI. Default "msb" on PATH.
	Binary string
	// Image is the OCI image. Default "alpine". OpenConfig.Image overrides
	// it.
	Image string
	// CPUs and Memory size the VM, e.g. 2 and "1G". Zero and empty use
	// msb's defaults.
	CPUs   int
	Memory string
}

// New validates the configuration.
func New(cfg Config) (kritrim.Sandbox, error) {
	if cfg.Binary == "" {
		cfg.Binary = "msb"
	}
	if cfg.Image == "" {
		cfg.Image = "alpine"
	}
	if cfg.CPUs < 0 {
		return nil, errors.New("microsandbox: CPUs must not be negative")
	}
	return &sandbox{cfg: cfg}, nil
}

type sandbox struct{ cfg Config }

func (s *sandbox) Open(ctx context.Context, opts kritrim.OpenConfig) (kritrim.Machine, error) {
	bin, err := exec.LookPath(s.cfg.Binary)
	if err != nil {
		return nil, fmt.Errorf("microsandbox: %s not found: %w", s.cfg.Binary, err)
	}
	var b [5]byte
	_, _ = rand.Read(b[:])
	name := "kritrim-" + hex.EncodeToString(b[:])
	image := s.cfg.Image
	if opts.Image != "" {
		image = opts.Image
	}
	// An image starting with "-" would be read by msb as a flag.
	if strings.HasPrefix(image, "-") {
		return nil, fmt.Errorf("microsandbox: image %q must not start with '-'", image)
	}
	args := []string{"create", image, "--name", name, "-q"}
	if !opts.Network {
		args = append(args, "--no-net")
	}
	if s.cfg.CPUs > 0 {
		args = append(args, "-c", fmt.Sprint(s.cfg.CPUs))
	}
	if s.cfg.Memory != "" {
		args = append(args, "-m", s.cfg.Memory)
	}
	m := &machine{bin: bin, name: name, env: opts.Env}
	if _, stderr, code, err := m.msb(ctx, nil, args...); err != nil || code != 0 {
		return nil, fmt.Errorf("microsandbox: create: %s %v", strings.TrimSpace(stderr), err)
	}
	return m, nil
}

type machine struct {
	bin  string
	name string
	env  []string

	mu     sync.Mutex
	closed bool
}

func (m *machine) msb(ctx context.Context, stdin []byte, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, m.bin, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	// The VM is untrusted: its output must not size the host's memory.
	out, errb := boxutil.NewCapped(), boxutil.NewCapped()
	cmd.Stdout, cmd.Stderr = out, errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code, err = ee.ExitCode(), nil
	}
	return out.String(), errb.String(), code, err
}

// defaultExecTimeout bounds a command that has no timeout of its own, so a
// hung command in the VM cannot block its caller forever.
const defaultExecTimeout = 10 * time.Minute

// usable reports an error once the machine is closed.
func (m *machine) usable() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("microsandbox: machine is closed")
	}
	return nil
}

// execArgs builds `msb exec` for one command.
func (m *machine) execArgs(flags []string, dir string, env []string, timeout time.Duration, argv []string) []string {
	args := append([]string{"exec", "-q"}, flags...)
	if dir != "" {
		args = append(args, "-w", dir)
	}
	for _, kv := range append(append([]string(nil), m.env...), env...) {
		args = append(args, "-e", kv)
	}
	if timeout > 0 {
		args = append(args, "--timeout", fmt.Sprintf("%ds", max(1, int(timeout.Seconds()))))
	}
	args = append(args, m.name, "--")
	return append(args, argv...)
}

func (m *machine) Exec(ctx context.Context, r kritrim.ExecRequest) (kritrim.ExecResult, error) {
	if len(r.Command) == 0 {
		return kritrim.ExecResult{}, errors.New("microsandbox: empty command")
	}
	if err := m.usable(); err != nil {
		return kritrim.ExecResult{}, err
	}
	if r.Timeout <= 0 {
		r.Timeout = defaultExecTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	flags := []string{"--no-tty"}
	if r.Stdin == nil {
		flags = []string{"--no-stdin"}
	}
	out, errb, code, err := m.msb(ctx, r.Stdin, m.execArgs(flags, r.Dir, r.Env, r.Timeout, r.Command)...)
	return kritrim.ExecResult{Stdout: out, Stderr: errb, Exit: code}, err
}

func (m *machine) Start(ctx context.Context, r kritrim.StartRequest) (kritrim.Process, error) {
	if len(r.Command) == 0 {
		return nil, errors.New("microsandbox: empty command")
	}
	if err := m.usable(); err != nil {
		return nil, err
	}
	// Not CommandContext: the process outlives the start context.
	cmd := exec.Command(m.bin, m.execArgs([]string{"--stream"}, r.Dir, r.Env, 0, r.Command)...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &process{cmd: cmd, in: in, out: out}, nil
}

func (m *machine) Read(ctx context.Context, path string) ([]byte, error) {
	if err := m.usable(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "kritrim-msb-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	local := filepath.Join(dir, "file")
	if _, stderr, code, err := m.msb(ctx, nil, "copy", m.name+":"+path, local); err != nil || code != 0 {
		return nil, fmt.Errorf("microsandbox: read %s: %s %v", path, strings.TrimSpace(stderr), err)
	}
	return os.ReadFile(local)
}

func (m *machine) Write(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	if err := m.usable(); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	dir, err := os.MkdirTemp("", "kritrim-msb-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	local := filepath.Join(dir, "file")
	if err := os.WriteFile(local, data, 0o600); err != nil {
		return err
	}
	if _, stderr, code, err := m.msb(ctx, nil, "copy", local, m.name+":"+path); err != nil || code != 0 {
		return fmt.Errorf("microsandbox: write %s: %s %v", path, strings.TrimSpace(stderr), err)
	}
	res, err := m.Exec(ctx, kritrim.ExecRequest{Command: []string{"chmod", fmt.Sprintf("%o", mode.Perm()), path}, Timeout: time.Minute})
	if err != nil || res.Exit != 0 {
		return fmt.Errorf("microsandbox: chmod %s: %s %v", path, res.Stderr, err)
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
	if _, stderr, code, err := m.msb(ctx, nil, "rm", "--force", "-q", m.name); err != nil || code != 0 {
		return fmt.Errorf("microsandbox: rm: %s %v", strings.TrimSpace(stderr), err)
	}
	return nil
}

type process struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
	once sync.Once
	err  error
}

func (p *process) Stdin() io.WriteCloser { return p.in }
func (p *process) Stdout() io.ReadCloser { return p.out }

func (p *process) Wait() error {
	p.once.Do(func() { p.err = p.cmd.Wait() })
	return p.err
}

func (p *process) Kill() error {
	if p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}
