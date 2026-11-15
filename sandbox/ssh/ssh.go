// Package ssh provides a kritrim sandbox on a remote machine over SSH.
//
// Commands are quoted for the remote shell. Host-key checking is enforced by
// default; extra SSH options are filtered to block directives that execute
// local commands.
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/internal/boxutil"
)

// Config configures an SSH sandbox.
type Config struct {
	Host         string // user@host
	WorkingDir   string
	IdentityFile string
	Port         int
	// AllowUnknownHosts disables host-key verification. Development only.
	AllowUnknownHosts bool
	// Options are extra -o options. Dangerous directives are rejected.
	Options []string
	Timeout time.Duration
}

// New returns an SSH sandbox.
func New(cfg Config) (kritrim.Sandbox, error) {
	if cfg.Host == "" || strings.ContainsAny(cfg.Host, " \t;|&") {
		return nil, errors.New("ssh: Host is required and must not contain shell characters")
	}
	if cfg.WorkingDir == "" {
		return nil, errors.New("ssh: WorkingDir is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Minute
	}
	for _, opt := range cfg.Options {
		if err := checkOption(opt); err != nil {
			return nil, err
		}
	}
	return &sandbox{cfg: cfg}, nil
}

// dangerous are options that run or load code on the local host. PKCS11Provider
// and SecurityKeyProvider dlopen a library; Match exec and Include can run
// commands or read further config.
var dangerous = []string{"proxycommand", "localcommand", "permitlocalcommand", "knownhostscommand", "remotecommand",
	"pkcs11provider", "securitykeyprovider", "match", "include"}

func checkOption(opt string) error {
	low := strings.ToLower(strings.TrimSpace(opt))
	for _, d := range dangerous {
		if strings.HasPrefix(low, d) || strings.Contains(low, d+"=") {
			return fmt.Errorf("ssh: option %q can run commands on the local host", opt)
		}
	}
	return nil
}

type sandbox struct{ cfg Config }

func (s *sandbox) Open(ctx context.Context, opts kritrim.OpenConfig) (kritrim.Machine, error) {
	if _, err := exec.LookPath("ssh"); err != nil {
		return nil, errors.New("ssh: client not found")
	}
	// Verify the connection early so failures surface at New.
	_, err := s.run(ctx, "true", nil, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("ssh: connect: %w", err)
	}
	return &machine{cfg: s.cfg}, nil
}

func (s *sandbox) args(remote string) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=15"}
	if s.cfg.AllowUnknownHosts {
		args = append(args, "-o", "StrictHostKeyChecking=no")
	} else {
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	}
	if s.cfg.IdentityFile != "" {
		args = append(args, "-i", s.cfg.IdentityFile)
	}
	if s.cfg.Port != 22 {
		args = append(args, "-p", strconv.Itoa(s.cfg.Port))
	}
	for _, o := range s.cfg.Options {
		args = append(args, "-o", o)
	}
	return append(args, s.cfg.Host, remote)
}

func (s *sandbox) run(ctx context.Context, remote string, stdin []byte, timeout time.Duration) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ssh", s.args(remote)...)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	out, errb := boxutil.NewCapped(), boxutil.NewCapped()
	cmd.Stdout, cmd.Stderr = out, errb
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

type machine struct{ cfg Config }

func (m *machine) Exec(ctx context.Context, req kritrim.ExecRequest) (kritrim.ExecResult, error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = m.cfg.Timeout
	}
	remote, err := m.command(req.Command, req.Dir, req.Env)
	if err != nil {
		return kritrim.ExecResult{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ssh", (&sandbox{cfg: m.cfg}).args(remote)...)
	if req.Stdin != nil {
		cmd.Stdin = strings.NewReader(string(req.Stdin))
	}
	out, errb := boxutil.NewCapped(), boxutil.NewCapped()
	cmd.Stdout, cmd.Stderr = out, errb
	err = cmd.Run()
	res := kritrim.ExecResult{Stdout: out.String(), Stderr: errb.String()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.Exit = ee.ExitCode()
			return res, nil
		}
		return res, err
	}
	return res, nil
}

func (m *machine) ExecStream(ctx context.Context, req kritrim.ExecRequest, emit func(stdout, stderr string)) (kritrim.ExecResult, error) {
	remote, err := m.command(req.Command, req.Dir, req.Env)
	if err != nil {
		return kritrim.ExecResult{}, err
	}
	cmd := exec.CommandContext(ctx, "ssh", (&sandbox{cfg: m.cfg}).args(remote)...)
	if req.Stdin != nil {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return kritrim.ExecResult{}, err
	}
	errb, err := cmd.StderrPipe()
	if err != nil {
		return kritrim.ExecResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return kritrim.ExecResult{}, err
	}
	var wg sync.WaitGroup
	o, e := boxutil.NewCapped(), boxutil.NewCapped()
	pump := func(rd io.Reader, dst *boxutil.Capped, isErr bool) {
		defer wg.Done()
		_ = boxutil.Lines(rd, dst, func(chunk string) {
			if emit == nil {
				return
			}
			if isErr {
				emit("", chunk)
			} else {
				emit(chunk, "")
			}
		})
	}
	wg.Add(2)
	go pump(out, o, false)
	go pump(errb, e, true)
	wg.Wait()
	code := 0
	if werr := cmd.Wait(); werr != nil {
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			code = ee.ExitCode()
		} else {
			return kritrim.ExecResult{}, werr
		}
	}
	return kritrim.ExecResult{Stdout: o.String(), Stderr: e.String(), Exit: code}, nil
}

func (m *machine) Start(ctx context.Context, req kritrim.StartRequest) (kritrim.Process, error) {
	remote, err := m.command(req.Command, req.Dir, req.Env)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("ssh", (&sandbox{cfg: m.cfg}).args(remote)...)
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
	return &proc{cmd: cmd, in: in, out: out}, nil
}

func (m *machine) Read(ctx context.Context, p string) ([]byte, error) {
	out, err := (&sandbox{cfg: m.cfg}).run(ctx, "cat "+quote(p), nil, m.cfg.Timeout)
	return []byte(out), err
}

func (m *machine) Write(ctx context.Context, p string, data []byte, mode fs.FileMode) error {
	cctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ssh", (&sandbox{cfg: m.cfg}).args("cat > "+quote(p))...)
	cmd.Stdin = strings.NewReader(string(data))
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh: write %s: %v: %s", p, err, errb.String())
	}
	// The remote umask decides the file's mode otherwise, so apply the
	// requested one. Nothing else is trusted to set it.
	if mode == 0 {
		mode = 0o644
	}
	if _, err := (&sandbox{cfg: m.cfg}).run(cctx, fmt.Sprintf("chmod %o %s", mode.Perm(), quote(p)), nil, m.cfg.Timeout); err != nil {
		return fmt.Errorf("ssh: chmod %s: %w", p, err)
	}
	return nil
}

func (m *machine) Close(ctx context.Context) error { return nil }

// envName is a valid shell variable name.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (m *machine) command(argv []string, dir string, env []string) (string, error) {
	var b strings.Builder
	b.WriteString("cd " + quote(firstNonEmpty(dir, m.cfg.WorkingDir)))
	for _, e := range env {
		// Each entry is NAME=value. The value is data, so it is quoted;
		// an entry that is not NAME=value is refused rather than guessed at.
		k, v, ok := strings.Cut(e, "=")
		if !ok || !envName.MatchString(k) {
			return "", errors.New("ssh: environment entries must be NAME=value")
		}
		b.WriteString(" && export " + k + "=" + quote(v))
	}
	if len(argv) == 0 {
		return b.String(), nil
	}
	b.WriteString(" && exec")
	for _, a := range argv {
		b.WriteString(" " + quote(a))
	}
	return b.String(), nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type proc struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
	mu   sync.Mutex
	err  error
	done bool
}

func (p *proc) Stdin() io.WriteCloser { return p.in }
func (p *proc) Stdout() io.ReadCloser { return p.out }

func (p *proc) Wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return p.err
	}
	p.done = true
	p.err = p.cmd.Wait()
	return p.err
}

func (p *proc) Kill() error {
	if p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}
