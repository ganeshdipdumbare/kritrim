package kritrim

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
)

// DockerConfig describes a Docker sandbox.
type DockerConfig struct {
	Image string
	// Network enables container networking. Default false.
	Network bool
}

// Docker returns a sandbox backed by the docker CLI. The image must provide
// /bin/sh. Containers are removed on Close.
func Docker(cfg DockerConfig) Sandbox { return &dockerSandbox{cfg: cfg} }

type dockerSandbox struct{ cfg DockerConfig }

func (d *dockerSandbox) Open(ctx context.Context, opts OpenConfig) (Machine, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, errors.New("kritrim: docker CLI not found")
	}
	image := d.cfg.Image
	if opts.Image != "" {
		image = opts.Image
	}
	if image == "" {
		return nil, errors.New("kritrim: Docker needs an image")
	}
	name := "kritrim-" + newID()[:10]
	args := []string{"run", "-d", "--rm", "--name", name}
	if !d.cfg.Network && !opts.Network {
		args = append(args, "--network", "none")
	}
	for _, e := range opts.Env {
		args = append(args, "-e", e)
	}
	args = append(args, image, "sh", "-c", "while true; do sleep 3600; done")
	if _, stderr, code, err := dockerRun(ctx, args, nil); err != nil || code != 0 {
		return nil, fmt.Errorf("kritrim: docker run: %s %w", stderr, err)
	}
	return &dockerMachine{name: name}, nil
}

type dockerMachine struct{ name string }

func (m *dockerMachine) Exec(ctx context.Context, r ExecRequest) (ExecResult, error) {
	if len(r.Command) == 0 {
		return ExecResult{}, errors.New("kritrim: empty command")
	}
	args := []string{"exec"}
	if r.Dir != "" {
		args = append(args, "-w", r.Dir)
	}
	for _, e := range r.Env {
		args = append(args, "-e", e)
	}
	args = append(args, m.name)
	args = append(args, r.Command...)
	out, errb, code, err := dockerRun(ctx, args, r.Stdin)
	return ExecResult{Stdout: out, Stderr: errb, Exit: code}, err
}

func (m *dockerMachine) Start(ctx context.Context, r StartRequest) (Process, error) {
	if len(r.Command) == 0 {
		return nil, errors.New("kritrim: empty command")
	}
	args := []string{"exec", "-i"}
	if r.Dir != "" {
		args = append(args, "-w", r.Dir)
	}
	for _, e := range r.Env {
		args = append(args, "-e", e)
	}
	args = append(args, m.name)
	args = append(args, r.Command...)
	cmd := exec.CommandContext(ctx, "docker", args...)
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
	return &dockerProc{cmd: cmd, in: in, out: out}, nil
}

func (m *dockerMachine) Read(ctx context.Context, p string) ([]byte, error) {
	out, stderr, code, err := dockerRun(ctx, []string{"cp", m.name + ":" + p, "-"}, nil)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("kritrim: docker cp read %s: %s %w", p, stderr, err)
	}
	return untarOne([]byte(out))
}

func (m *dockerMachine) Write(ctx context.Context, p string, data []byte, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	dir, base := path.Split(p)
	if dir != "" {
		if _, stderr, code, err := dockerRun(ctx, []string{"exec", m.name, "mkdir", "-p", strings.TrimSuffix(dir, "/")}, nil); err != nil || code != 0 {
			return fmt.Errorf("kritrim: docker mkdir %s: %s %w", dir, stderr, err)
		}
	}
	blob, err := tarOne(base, data, mode)
	if err != nil {
		return err
	}
	dest := m.name + ":" + path.Dir(p)
	if _, stderr, code, err := dockerRun(ctx, []string{"cp", "-", dest}, blob); err != nil || code != 0 {
		return fmt.Errorf("kritrim: docker cp write %s: %s %w", p, stderr, err)
	}
	return nil
}

func (m *dockerMachine) Close(ctx context.Context) error {
	_, _, _, err := dockerRun(ctx, []string{"rm", "-f", m.name}, nil)
	return err
}

type dockerProc struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
	mu   sync.Mutex
	err  error
	done bool
}

func (p *dockerProc) Stdin() io.WriteCloser { return p.in }
func (p *dockerProc) Stdout() io.ReadCloser { return p.out }

func (p *dockerProc) Wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return p.err
	}
	p.done = true
	p.err = p.cmd.Wait()
	return p.err
}

func (p *dockerProc) Kill() error {
	if p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

func dockerRun(ctx context.Context, args []string, stdin []byte) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
			err = nil
		}
	}
	return out.String(), errb.String(), code, err
}

func tarOne(name string, data []byte, mode fs.FileMode) ([]byte, error) {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	h := &tar.Header{Name: name, Mode: int64(mode), Size: int64(len(data)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(h); err != nil {
		return nil, err
	}
	if _, err := tw.Write(data); err != nil {
		return nil, err
	}
	return b.Bytes(), tw.Close()
}

func untarOne(b []byte) ([]byte, error) {
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("kritrim: empty archive")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
}
