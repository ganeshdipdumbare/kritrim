package kritrim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim/internal/boxutil"
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

type localMachine struct {
	// confine, when set, runs every command under Confined's kernel
	// confinement by re-executing self.
	confine   *confineSpec
	self      string
	workspace string
}

// command builds the exec.Cmd for argv. ctx nil means a process that
// outlives its start context.
func (m *localMachine) command(ctx context.Context, argv []string, dir string, env []string) (*exec.Cmd, error) {
	if m.confine != nil {
		return m.confinedCmd(ctx, argv, dir, env)
	}
	var cmd *exec.Cmd
	if ctx != nil {
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
	} else {
		cmd = exec.Command(argv[0], argv[1:]...)
	}
	cmd.Dir, cmd.Env = dir, childEnv(env)
	return cmd, nil
}

func (m *localMachine) Exec(ctx context.Context, r ExecRequest) (ExecResult, error) {
	// The agent checks its allowlist before it reaches a machine. The check
	// repeats here so a caller that reaches the host through the machine
	// directly is held to the same list.
	if r.Allow != nil {
		if err := checkCommand(execRequest{Command: r.Command, Allow: r.Allow}); err != nil {
			return ExecResult{}, err
		}
	}
	res, err := m.run(ctx, execRequest{Command: r.Command, Dir: r.Dir, Env: r.Env,
		Stdin: r.Stdin, Timeout: r.Timeout}, false)
	return ExecResult(res), err
}

func (m *localMachine) ExecStream(ctx context.Context, r ExecRequest, emit func(stdout, stderr string)) (ExecResult, error) {
	if len(r.Command) == 0 {
		return ExecResult{}, fmt.Errorf("kritrim: empty command")
	}
	// The same timeout Exec honors: a streamed command is not exempt from it.
	ctx, cancel := withTimeoutOr(ctx, r.Timeout, 60*time.Second)
	defer cancel()
	if r.Allow != nil {
		if err := checkCommand(execRequest{Command: r.Command, Allow: r.Allow}); err != nil {
			return ExecResult{}, err
		}
	}
	cmd, err := m.command(ctx, r.Command, r.Dir, r.Env)
	if err != nil {
		return ExecResult{}, err
	}
	if r.Stdin != nil {
		cmd.Stdin = bytes.NewReader(r.Stdin)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return ExecResult{}, err
	}
	errb, err := cmd.StderrPipe()
	if err != nil {
		return ExecResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return ExecResult{}, err
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
			return ExecResult{}, werr
		}
	}
	return ExecResult{Stdout: o.String(), Stderr: e.String(), Exit: code}, nil
}

func (m *localMachine) Start(ctx context.Context, r StartRequest) (Process, error) {
	if len(r.Command) == 0 {
		return nil, fmt.Errorf("kritrim: empty command")
	}
	if r.Allow != nil {
		if err := checkCommand(execRequest{Command: r.Command, Allow: r.Allow}); err != nil {
			return nil, err
		}
	}
	//nolint:contextcheck // the process outlives the start context.
	cmd, err := m.command(nil, r.Command, r.Dir, r.Env)
	if err != nil {
		return nil, err
	}
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
	// Like every other machine's Read, a file past the output limit, or a
	// device, is an error, not a read into host memory.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("kritrim: %s is not a regular file", path)
	}
	if fi.Size() > boxutil.MaxOutput {
		return nil, fmt.Errorf("kritrim: %s is larger than %d bytes", path, boxutil.MaxOutput)
	}
	return io.ReadAll(io.LimitReader(f, boxutil.MaxOutput+1))
}

func (m *localMachine) Write(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	return os.WriteFile(path, data, mode)
}

func (m *localMachine) Close(ctx context.Context) error {
	if m.workspace != "" {
		return os.RemoveAll(m.workspace)
	}
	return nil
}

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
