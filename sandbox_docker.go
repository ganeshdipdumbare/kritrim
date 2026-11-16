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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim/internal/boxutil"
)

// DockerConfig describes a Docker sandbox.
type DockerConfig struct {
	Image string
	// Container attaches to an existing running container instead of
	// creating one. Close does not remove it.
	Container string
	// Network enables container networking. Default false.
	Network bool

	// The settings below harden containers kritrim creates (not attached
	// ones). By default every capability is dropped, privilege escalation
	// is disabled and memory, CPU and process counts are capped.

	// User runs the container as this user ("uid:gid" or a name). Default:
	// the image's user. A non-root user is recommended.
	User string
	// Memory caps container memory, e.g. "512m". Default "2g"; "none"
	// removes the cap.
	Memory string
	// CPUs caps CPU, e.g. "1.5". Default "2"; "none" removes the cap.
	CPUs string
	// PidsLimit caps processes. Default 512; -1 removes the cap.
	PidsLimit int
	// ReadOnly mounts the root filesystem read-only with a writable /tmp.
	// The box binary and Write go to /tmp.
	ReadOnly bool
	// CapAdd re-adds Linux capabilities, e.g. "CHOWN". Default none.
	CapAdd []string
}

// dockerRunArgs builds the hardened `docker run` command line.
func dockerRunArgs(cfg DockerConfig, name, image string, opts OpenConfig, envPath string) []string {
	args := []string{"run", "-d", "--rm", "--name", name,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges"}
	for _, c := range cfg.CapAdd {
		args = append(args, "--cap-add", c)
	}
	if !cfg.Network && !opts.Network {
		args = append(args, "--network", "none")
	}
	switch cfg.Memory {
	case "none":
	case "":
		args = append(args, "--memory", "2g", "--memory-swap", "2g")
	default:
		args = append(args, "--memory", cfg.Memory, "--memory-swap", cfg.Memory)
	}
	switch cfg.CPUs {
	case "none":
	case "":
		args = append(args, "--cpus", "2")
	default:
		args = append(args, "--cpus", cfg.CPUs)
	}
	switch {
	case cfg.PidsLimit < 0:
	case cfg.PidsLimit == 0:
		args = append(args, "--pids-limit", "512")
	default:
		args = append(args, "--pids-limit", fmt.Sprint(cfg.PidsLimit))
	}
	if cfg.User != "" {
		args = append(args, "--user", cfg.User)
	}
	if cfg.ReadOnly {
		args = append(args, "--read-only", "--tmpfs", "/tmp:rw,exec,nosuid,size=512m")
	}
	if envPath != "" {
		args = append(args, "--env-file", envPath)
	}
	// "--" ends option parsing, so an image name can never be read as a flag.
	return append(args, "--", image, "sh", "-c", "while true; do sleep 3600; done")
}

// Docker returns a sandbox backed by the docker CLI. The image must provide
// /bin/sh. Containers are removed on Close.
func Docker(cfg DockerConfig) Sandbox { return &dockerSandbox{cfg: cfg} }

type dockerSandbox struct{ cfg DockerConfig }

func (d *dockerSandbox) Open(ctx context.Context, opts OpenConfig) (Machine, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, errors.New("kritrim: docker CLI not found")
	}
	if d.cfg.Container != "" {
		if err := checkRef("container", d.cfg.Container); err != nil {
			return nil, err
		}
		out, stderr, code, err := dockerRun(ctx, []string{"inspect", "-f", "{{.State.Running}}", "--", d.cfg.Container}, nil)
		if err != nil || code != 0 || strings.TrimSpace(out) != "true" {
			return nil, fmt.Errorf("kritrim: container %q is not running: %s %w", d.cfg.Container, stderr, err)
		}
		return &dockerMachine{name: d.cfg.Container, external: true}, nil
	}
	image := d.cfg.Image
	if opts.Image != "" {
		image = opts.Image
	}
	if image == "" {
		return nil, errors.New("kritrim: Docker needs an image")
	}
	if err := checkRef("image", image); err != nil {
		return nil, err
	}
	for _, c := range d.cfg.CapAdd {
		if err := checkRef("capability", c); err != nil {
			return nil, err
		}
	}
	if err := checkEnv(opts.Env); err != nil {
		return nil, err
	}
	name := "kritrim-" + newID()[:10]
	envPath, cleanup, err := envFile(opts.Env)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	args := dockerRunArgs(d.cfg, name, image, opts, envPath)
	if _, stderr, code, err := dockerRunWith(ctx, args, nil, opts.Env); err != nil || code != 0 {
		return nil, fmt.Errorf("kritrim: docker run: %s %w", stderr, err)
	}
	return &dockerMachine{name: name, viaExec: d.cfg.ReadOnly || d.cfg.User != ""}, nil
}

type dockerMachine struct {
	name     string
	external bool
	// viaExec moves files with exec + cat instead of docker cp: cp cannot
	// reach a tmpfs, and exec writes as the container user.
	viaExec bool
}

func (m *dockerMachine) Exec(ctx context.Context, r ExecRequest) (ExecResult, error) {
	if len(r.Command) == 0 {
		return ExecResult{}, errors.New("kritrim: empty command")
	}
	if err := checkEnv(r.Env); err != nil {
		return ExecResult{}, err
	}
	ctx, cancel := withTimeoutOr(ctx, r.Timeout, defaultDockerExecTimeout)
	defer cancel()
	envPath, cleanup, err := envFile(r.Env)
	if err != nil {
		return ExecResult{}, err
	}
	defer cleanup()
	out, errb, code, err := dockerRunWith(ctx, dockerExecArgs(m.name, r, envPath), r.Stdin, r.Env)
	return ExecResult{Stdout: out.String(), Stderr: errb, Exit: code}, err
}

func (m *dockerMachine) ExecStream(ctx context.Context, r ExecRequest, emit func(stdout, stderr string)) (ExecResult, error) {
	if len(r.Command) == 0 {
		return ExecResult{}, errors.New("kritrim: empty command")
	}
	if err := checkEnv(r.Env); err != nil {
		return ExecResult{}, err
	}
	ctx, cancel := withTimeoutOr(ctx, r.Timeout, defaultDockerExecTimeout)
	defer cancel()
	envPath, cleanup, err := envFile(r.Env)
	if err != nil {
		return ExecResult{}, err
	}
	defer cleanup()
	args := dockerExecArgs(m.name, r, envPath)
	cmd := exec.CommandContext(ctx, "docker", args...)
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

// dockerExecArgs builds `docker exec` for a request. Without -i docker
// never forwards stdin, so a command reading it would see nothing.
func dockerExecArgs(name string, r ExecRequest, envPath string) []string {
	args := []string{"exec"}
	if r.Stdin != nil {
		args = append(args, "-i")
	}
	if r.Dir != "" {
		args = append(args, "-w", r.Dir)
	}
	if envPath != "" {
		args = append(args, "--env-file", envPath)
	}
	// docker exec stops reading flags at the container name, so command
	// arguments are never options. (A "--" here would become the command.)
	args = append(args, name)
	return append(args, r.Command...)
}

// envFile writes the sandbox env to a private file for `--env-file`, so
// values stay off argv (visible to ps) and out of the docker client's own
// environment. The caller removes the file with cleanup. It returns no path
// when there is no env.
func envFile(env []string) (string, func(), error) {
	noop := func() {}
	if len(env) == 0 {
		return "", noop, nil
	}
	f, err := os.CreateTemp("", "kritrim-env-*")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	for _, e := range env {
		if _, err := fmt.Fprintln(f, e); err != nil {
			f.Close()
			cleanup()
			return "", noop, err
		}
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", noop, err
	}
	return f.Name(), cleanup, nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkEnv refuses entries that are not NAME=value with a plain name, or
// whose value would break the env file's one-entry-per-line format.
func checkEnv(env []string) error {
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		if !envName.MatchString(k) || strings.ContainsAny(v, "\r\n") {
			return errors.New("kritrim: environment entries must be NAME=value on one line")
		}
	}
	return nil
}

// checkRef refuses names the docker CLI would read as an option.
func checkRef(what, v string) error {
	if strings.HasPrefix(v, "-") {
		return fmt.Errorf("kritrim: docker %s %q must not start with '-'", what, v)
	}
	return nil
}

// defaultDockerExecTimeout bounds a docker exec that has no timeout of its own.
const defaultDockerExecTimeout = 10 * time.Minute

// withTimeoutOr bounds a request by its timeout, or by the default when it has
// none.
func withTimeoutOr(ctx context.Context, d, def time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = def
	}
	return context.WithTimeout(ctx, d)
}

// withTimeout bounds a request by its own timeout, if it has one.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

func (m *dockerMachine) Start(ctx context.Context, r StartRequest) (Process, error) {
	if len(r.Command) == 0 {
		return nil, errors.New("kritrim: empty command")
	}
	if err := checkEnv(r.Env); err != nil {
		return nil, err
	}
	args := []string{"exec", "-i"}
	if r.Dir != "" {
		args = append(args, "-w", r.Dir)
	}
	envPath, cleanup, err := envFile(r.Env)
	if err != nil {
		return nil, err
	}
	handed := false // true once the process owns the file
	defer func() {
		if !handed {
			cleanup()
		}
	}()
	if envPath != "" {
		args = append(args, "--env-file", envPath)
	}
	args = append(args, m.name)
	args = append(args, r.Command...)
	// Not CommandContext: the process outlives the start context.
	cmd := exec.Command("docker", args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = boxutil.SafeStderr()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	handed = true
	return &dockerProc{cmd: cmd, in: in, out: out, cleanup: cleanup}, nil
}

func (m *dockerMachine) Read(ctx context.Context, p string) ([]byte, error) {
	if m.viaExec {
		out, stderr, code, err := dockerRunWith(ctx, []string{"exec", m.name, "cat", "--", p}, nil, nil)
		if err != nil || code != 0 {
			return nil, fmt.Errorf("kritrim: docker read %s: %s %w", p, stderr, err)
		}
		// Raw bytes, and an error rather than a silently cut file.
		if out.Truncated() {
			return nil, fmt.Errorf("kritrim: %s is larger than %d bytes", p, boxutil.MaxOutput)
		}
		return out.Bytes(), nil
	}
	out, stderr, code, err := dockerRunWith(ctx, []string{"cp", m.name + ":" + p, "-"}, nil, nil)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("kritrim: docker cp read %s: %s %w", p, stderr, err)
	}
	if out.Truncated() {
		return nil, fmt.Errorf("kritrim: %s is larger than %d bytes", p, boxutil.MaxOutput)
	}
	return untarOne(out.Bytes())
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
	if m.viaExec {
		script := `cat > "$1" && chmod "$2" "$1"`
		args := []string{"exec", "-i", m.name, "sh", "-c", script, "sh", p, fmt.Sprintf("%o", mode.Perm())}
		if _, stderr, code, err := dockerRun(ctx, args, data); err != nil || code != 0 {
			return fmt.Errorf("kritrim: docker write %s: %s %w", p, stderr, err)
		}
		return nil
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
	if m.external {
		return nil
	}
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
	// cleanup removes the env file. docker reads it after the process starts,
	// so it is removed only once the process has ended.
	cleanup func()
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
	p.cleanup()
	return p.err
}

func (p *dockerProc) Kill() error {
	// A killed process may never be waited on, and the env file it was given
	// holds secrets. The CLI has read it by now or is dead, so remove it here.
	defer p.cleanup()
	if p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

func dockerRun(ctx context.Context, args []string, stdin []byte) (string, string, int, error) {
	out, errb, code, err := dockerRunWith(ctx, args, stdin, nil)
	return out.String(), errb, code, err
}

// dockerRunWith runs the docker CLI with capped output. The values in env are
// removed from any stderr returned, since docker can echo them in an error.
func dockerRunWith(ctx context.Context, args []string, stdin []byte, env []string) (*boxutil.Capped, string, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, errb := boxutil.NewCapped(), boxutil.NewCapped()
	cmd.Stdout, cmd.Stderr = out, errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
			err = nil
		}
	}
	stderr := errb.String()
	for _, e := range env {
		if _, v, ok := strings.Cut(e, "="); ok && v != "" {
			stderr = strings.ReplaceAll(stderr, v, "[redacted]")
		}
	}
	return out, stderr, code, err
}

func tarOne(name string, data []byte, mode fs.FileMode) ([]byte, error) {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	// Only the permission bits belong in a tar header. Go type bits in the
	// mode would make the archive's entry something other than a file.
	h := &tar.Header{Name: name, Mode: int64(mode.Perm()), Size: int64(len(data)), Typeflag: tar.TypeReg}
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
