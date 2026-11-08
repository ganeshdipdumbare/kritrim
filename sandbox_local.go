package kritrim

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sync"
)

// Local returns a sandbox that runs on this host. It is not an isolation
// boundary: use it for development, or for the whole-agent "local" box.
func Local() Sandbox { return localSandbox{} }

type localSandbox struct{}

func (localSandbox) Open(ctx context.Context, opts OpenConfig) (Machine, error) {
	if opts.Image != "" {
		return nil, fmt.Errorf("kritrim: Local ignores Image")
	}
	return &localMachine{}, nil
}

type localMachine struct{}

func (m *localMachine) Exec(ctx context.Context, r ExecRequest) (ExecResult, error) {
	res, err := localExec(ctx, execRequest{Command: r.Command, Dir: r.Dir, Env: r.Env,
		Stdin: r.Stdin, Timeout: r.Timeout}, false)
	return ExecResult{Stdout: res.Stdout, Stderr: res.Stderr, Exit: res.Exit}, err
}

func (m *localMachine) Start(ctx context.Context, r StartRequest) (Process, error) {
	if len(r.Command) == 0 {
		return nil, fmt.Errorf("kritrim: empty command")
	}
	cmd := exec.Command(r.Command[0], r.Command[1:]...)
	cmd.Dir, cmd.Env = r.Dir, append(os.Environ(), r.Env...)
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
	return &localProc{cmd: cmd, in: in, out: out}, nil
}

func (m *localMachine) Read(ctx context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (m *localMachine) Write(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	return os.WriteFile(path, data, mode)
}

func (m *localMachine) Close(ctx context.Context) error { return nil }

type localProc struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
	mu   sync.Mutex
	err  error
	done bool
}

func (p *localProc) Stdin() io.WriteCloser { return p.in }
func (p *localProc) Stdout() io.ReadCloser { return p.out }

func (p *localProc) Wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return p.err
	}
	p.done = true
	p.err = p.cmd.Wait()
	return p.err
}

func (p *localProc) Kill() error {
	if p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}
