// Package tensorlake provides a kritrim sandbox on Tensorlake sandboxes
// (tensorlake.ai).
//
//	box, err := tensorlake.New(tensorlake.Config{Image: "tensorlake/ubuntu-minimal"})
//
// Each Open creates a sandbox through the Tensorlake API and Close
// terminates it. Commands, files and long-lived processes go to the
// sandbox proxy; processes get a stdin pipe and streamed output, so a
// Tensorlake sandbox can host a sealed agent. Output arrives line by line,
// which suits the newline-delimited JSON a sealed box speaks. The API key
// stays on the host.
package tensorlake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/internal/boxutil"
)

// Config configures Tensorlake sandboxes.
type Config struct {
	// APIKey defaults to the TENSORLAKE_API_KEY environment variable.
	APIKey string
	// Image is the sandbox image. Default "tensorlake/ubuntu-minimal".
	// OpenConfig.Image overrides it.
	Image string
	// APIURL defaults to https://api.tensorlake.ai.
	APIURL string
	// ProxyURL is used when the API does not return a sandbox URL.
	// Default https://sandbox.tensorlake.ai.
	ProxyURL string
	// Lifetime is how long Tensorlake keeps a sandbox if Close is never
	// called. Default one hour.
	Lifetime time.Duration
	Client   *http.Client
	// PollInterval is how often Open checks a sandbox that is still
	// starting. Default two seconds.
	PollInterval time.Duration
}

// New validates the configuration.
func New(cfg Config) (kritrim.Sandbox, error) {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("TENSORLAKE_API_KEY")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("tensorlake: APIKey or TENSORLAKE_API_KEY is required")
	}
	if cfg.Image == "" {
		cfg.Image = "tensorlake/ubuntu-minimal"
	}
	if cfg.APIURL == "" {
		cfg.APIURL = "https://api.tensorlake.ai"
	}
	if cfg.ProxyURL == "" {
		cfg.ProxyURL = "https://sandbox.tensorlake.ai"
	}
	if cfg.Lifetime <= 0 {
		cfg.Lifetime = time.Hour
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	auth := map[string]string{"Authorization": "Bearer " + cfg.APIKey}
	return &sandbox{cfg: cfg, api: boxutil.NewClient("tensorlake", cfg.Client, auth)}, nil
}

type sandbox struct {
	cfg Config
	api *boxutil.Client
}

type sandboxInfo struct {
	SandboxID  string `json:"sandbox_id"`
	ID         string `json:"id"`
	Status     string `json:"status"`
	SandboxURL string `json:"sandbox_url"`
	Reason     string `json:"reason"`
}

func (s *sandbox) Open(ctx context.Context, opts kritrim.OpenConfig) (kritrim.Machine, error) {
	image := s.cfg.Image
	if opts.Image != "" {
		image = opts.Image
	}
	var info sandboxInfo
	err := s.api.JSON(ctx, http.MethodPost, boxutil.Join(s.cfg.APIURL, "sandboxes"), map[string]any{
		"image":        image,
		"timeout_secs": int(s.cfg.Lifetime.Seconds()),
		"network":      map[string]any{"allow_internet_access": opts.Network},
	}, &info, nil)
	var se *boxutil.StatusError
	if errors.As(err, &se) && se.Code == http.StatusGatewayTimeout {
		_ = json.Unmarshal([]byte(se.Body), &info)
		err = nil
	}
	if err != nil {
		return nil, fmt.Errorf("tensorlake: create sandbox: %w", err)
	}
	if info.SandboxID == "" {
		info.SandboxID = info.ID
	}
	if info.SandboxID == "" {
		return nil, errors.New("tensorlake: create sandbox: no sandbox id")
	}
	m := &machine{s: s, id: info.SandboxID}
	for info.Status != "running" {
		switch info.Status {
		case "failed", "terminated", "error":
			_ = m.Close(context.WithoutCancel(ctx))
			return nil, fmt.Errorf("tensorlake: sandbox %s %s: %s", info.SandboxID, info.Status, info.Reason)
		}
		select {
		case <-ctx.Done():
			_ = m.Close(context.WithoutCancel(ctx))
			return nil, ctx.Err()
		case <-time.After(s.cfg.PollInterval):
		}
		url := info.SandboxURL
		if err := s.api.JSON(ctx, http.MethodGet, boxutil.Join(s.cfg.APIURL, "sandboxes", info.SandboxID), nil, &info, nil); err != nil {
			_ = m.Close(context.WithoutCancel(ctx))
			return nil, fmt.Errorf("tensorlake: sandbox status: %w", err)
		}
		if info.SandboxURL == "" {
			info.SandboxURL = url
		}
		if info.SandboxID == "" {
			info.SandboxID = m.id
		}
	}
	proxy := info.SandboxURL
	if proxy == "" {
		proxy = s.cfg.ProxyURL
	}
	headers := map[string]string{"Authorization": "Bearer " + s.cfg.APIKey, "X-Tensorlake-Sandbox-Id": m.id}
	m.proxy = strings.TrimSuffix(proxy, "/")
	m.rpc = boxutil.NewClient("tensorlake", s.cfg.Client, headers)
	m.stream = boxutil.NewClient("tensorlake", s.cfg.Client, headers)
	m.stream.HTTP.Timeout = 0
	return m, nil
}

type machine struct {
	s      *sandbox
	id     string
	proxy  string
	rpc    *boxutil.Client
	stream *boxutil.Client

	mu     sync.Mutex
	closed bool
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

func payload(argv []string, dir string, env []string) map[string]any {
	p := map[string]any{"command": argv[0], "args": argv[1:], "env": envMap(env)}
	if dir != "" {
		p["working_dir"] = dir
	}
	return p
}

// runEvent is one event of /api/v1/processes/run or an output follow.
type runEvent struct {
	PID      *int64  `json:"pid"`
	Line     *string `json:"line"`
	Stream   string  `json:"stream"`
	ExitCode *int    `json:"exit_code"`
	Signal   *int    `json:"signal"`
}

func (m *machine) Exec(ctx context.Context, r kritrim.ExecRequest) (kritrim.ExecResult, error) {
	if len(r.Command) == 0 {
		return kritrim.ExecResult{}, errors.New("tensorlake: empty command")
	}
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	if r.Stdin != nil {
		return m.execWithStdin(ctx, r)
	}
	p := payload(r.Command, r.Dir, r.Env)
	if r.Timeout > 0 {
		p["timeout"] = r.Timeout.Seconds()
	}
	b, _ := json.Marshal(p)
	resp, err := m.stream.Do(ctx, http.MethodPost, m.proxy+"/api/v1/processes/run", bytes.NewReader(b),
		map[string]string{"Content-Type": "application/json", "Accept": "text/event-stream"})
	if err != nil {
		return kritrim.ExecResult{}, fmt.Errorf("tensorlake: run: %w", err)
	}
	defer resp.Body.Close()
	var out, errb strings.Builder
	res := kritrim.ExecResult{Exit: -1}
	exited := false
	err = boxutil.SSE(resp.Body, func(data []byte) bool {
		var e runEvent
		if json.Unmarshal(data, &e) != nil {
			return true
		}
		switch {
		case e.Line != nil && e.Stream == "stderr":
			errb.WriteString(*e.Line + "\n")
		case e.Line != nil:
			out.WriteString(*e.Line + "\n")
		case e.ExitCode != nil:
			res.Exit, exited = *e.ExitCode, true
		case e.Signal != nil:
			res.Exit, exited = 128+*e.Signal, true
		}
		return true
	})
	res.Stdout, res.Stderr = out.String(), errb.String()
	if err == nil && !exited {
		err = errors.New("tensorlake: process stream ended without an exit event")
	}
	return res, err
}

// execWithStdin runs a command whose stdin is given: start it with a pipe,
// write and close stdin, and collect its output.
func (m *machine) execWithStdin(ctx context.Context, r kritrim.ExecRequest) (kritrim.ExecResult, error) {
	pid, err := m.startProcess(ctx, r.Command, r.Dir, r.Env)
	if err != nil {
		return kritrim.ExecResult{}, err
	}
	if len(r.Stdin) > 0 {
		if err := m.writeStdin(ctx, pid, r.Stdin); err != nil {
			return kritrim.ExecResult{}, err
		}
	}
	if err := m.closeStdin(ctx, pid); err != nil {
		return kritrim.ExecResult{}, err
	}
	var out, errb strings.Builder
	if err := m.follow(ctx, pid, "output", func(e runEvent) {
		if e.Stream == "stderr" {
			errb.WriteString(*e.Line + "\n")
		} else {
			out.WriteString(*e.Line + "\n")
		}
	}); err != nil {
		return kritrim.ExecResult{}, err
	}
	code, err := m.exitCode(ctx, pid)
	return kritrim.ExecResult{Stdout: out.String(), Stderr: errb.String(), Exit: code}, err
}

func (m *machine) startProcess(ctx context.Context, argv []string, dir string, env []string) (int64, error) {
	p := payload(argv, dir, env)
	p["stdin_mode"] = "pipe"
	var info struct {
		PID int64 `json:"pid"`
	}
	if err := m.rpc.JSON(ctx, http.MethodPost, m.proxy+"/api/v1/processes", p, &info, nil); err != nil {
		return 0, fmt.Errorf("tensorlake: start: %w", err)
	}
	return info.PID, nil
}

func (m *machine) procURL(pid int64, rest string) string {
	return m.proxy + "/api/v1/processes/" + strconv.FormatInt(pid, 10) + rest
}

func (m *machine) writeStdin(ctx context.Context, pid int64, b []byte) error {
	resp, err := m.rpc.Do(ctx, http.MethodPost, m.procURL(pid, "/stdin"), bytes.NewReader(b),
		map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return fmt.Errorf("tensorlake: stdin: %w", err)
	}
	return resp.Body.Close()
}

func (m *machine) closeStdin(ctx context.Context, pid int64) error {
	resp, err := m.rpc.Do(ctx, http.MethodPost, m.procURL(pid, "/stdin/close"), nil, nil)
	if err != nil {
		return fmt.Errorf("tensorlake: close stdin: %w", err)
	}
	return resp.Body.Close()
}

// follow streams one of a process's output channels ("stdout", "stderr"
// or "output") until the process ends.
func (m *machine) follow(ctx context.Context, pid int64, channel string, fn func(runEvent)) error {
	resp, err := m.stream.Do(ctx, http.MethodGet, m.procURL(pid, "/"+channel+"/follow"), nil,
		map[string]string{"Accept": "text/event-stream"})
	if err != nil {
		return fmt.Errorf("tensorlake: follow: %w", err)
	}
	defer resp.Body.Close()
	return boxutil.SSE(resp.Body, func(data []byte) bool {
		var e runEvent
		if json.Unmarshal(data, &e) == nil && e.Line != nil {
			fn(e)
		}
		return true
	})
}

// exitCode waits for a process to finish and returns its exit code.
func (m *machine) exitCode(ctx context.Context, pid int64) (int, error) {
	for {
		var info struct {
			Status   string `json:"status"`
			ExitCode *int   `json:"exit_code"`
			Signal   *int   `json:"signal"`
		}
		if err := m.rpc.JSON(ctx, http.MethodGet, m.procURL(pid, ""), nil, &info, nil); err != nil {
			return -1, fmt.Errorf("tensorlake: process status: %w", err)
		}
		switch {
		case info.ExitCode != nil:
			return *info.ExitCode, nil
		case info.Signal != nil:
			return 128 + *info.Signal, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (m *machine) Start(ctx context.Context, r kritrim.StartRequest) (kritrim.Process, error) {
	if len(r.Command) == 0 {
		return nil, errors.New("tensorlake: empty command")
	}
	pid, err := m.startProcess(ctx, r.Command, r.Dir, r.Env)
	if err != nil {
		return nil, err
	}
	// The process outlives ctx, which only bounds starting it.
	life, stop := context.WithCancel(context.WithoutCancel(ctx))
	p := boxutil.NewProcess(
		func(b []byte) error { return m.writeStdin(life, pid, b) },
		func() error { return m.closeStdin(life, pid) },
		func() error {
			defer stop()
			resp, err := m.rpc.Do(life, http.MethodDelete, m.procURL(pid, ""), nil, nil)
			if err == nil {
				resp.Body.Close()
			}
			return err
		},
	)
	go func() {
		_ = m.follow(life, pid, "stderr", func(e runEvent) { fmt.Fprintln(os.Stderr, *e.Line) })
	}()
	go func() {
		defer stop()
		err := m.follow(life, pid, "stdout", func(e runEvent) { _ = p.Output([]byte(*e.Line + "\n")) })
		if err != nil {
			p.Exit(err)
			return
		}
		code, err := m.exitCode(life, pid)
		switch {
		case err != nil:
			p.Exit(err)
		case code != 0:
			p.Exit(&boxutil.ExitError{Code: code})
		default:
			p.Exit(nil)
		}
	}()
	return p, nil
}

func (m *machine) fileURL(path string) string {
	return m.proxy + "/api/v1/files?" + url.Values{"path": {path}}.Encode()
}

func (m *machine) Read(ctx context.Context, path string) ([]byte, error) {
	resp, err := m.rpc.Do(ctx, http.MethodGet, m.fileURL(path), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("tensorlake: read %s: %w", path, err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (m *machine) Write(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	resp, err := m.rpc.Do(ctx, http.MethodPut, m.fileURL(path), bytes.NewReader(data),
		map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return fmt.Errorf("tensorlake: write %s: %w", path, err)
	}
	resp.Body.Close()
	res, err := m.Exec(ctx, kritrim.ExecRequest{Command: []string{"chmod", fmt.Sprintf("%o", mode.Perm()), path}, Timeout: time.Minute})
	if err != nil || res.Exit != 0 {
		return fmt.Errorf("tensorlake: chmod %s: %s %v", path, res.Stderr, err)
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
	err := m.s.api.JSON(ctx, http.MethodDelete, boxutil.Join(m.s.cfg.APIURL, "sandboxes", m.id), nil, nil, nil)
	var se *boxutil.StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return nil
	}
	return err
}
