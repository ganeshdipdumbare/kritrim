// Package daytona provides a kritrim sandbox on Daytona (daytona.io).
//
//	box, err := daytona.New(daytona.Config{})
//	agent, err := kritrim.New(kritrim.Config{Model: model, Tools: tools, Sandbox: box})
//
// Each Open creates a sandbox through the Daytona API and Close deletes it.
// Commands and files go to the sandbox toolbox. The toolbox runs a command
// to completion and returns its output, with stdout and stderr merged into
// ExecResult.Stdout; it cannot stream a process's stdin and stdout, so
// Start returns errors.ErrUnsupported and a Daytona sandbox cannot host a
// sealed agent. Use it for Config.Sandbox. The API key stays on the host.
package daytona

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/internal/boxutil"
)

// Config configures Daytona sandboxes.
type Config struct {
	// APIKey defaults to the DAYTONA_API_KEY environment variable.
	APIKey string
	// Organization sets X-Daytona-Organization-ID when the key belongs to
	// several organizations.
	Organization string
	// Snapshot is the sandbox snapshot. Empty uses Daytona's default.
	// OpenConfig.Image overrides it.
	Snapshot string
	// APIURL defaults to https://app.daytona.io/api.
	APIURL string
	// ToolboxURL is used when the API does not return a toolbox proxy URL.
	// Default https://proxy.app.daytona.io/toolbox.
	ToolboxURL string
	// AutoStop stops an idle sandbox, which also deletes it, if Close is
	// never called. Default 15 minutes.
	AutoStop time.Duration
	Client   *http.Client
	// PollInterval is how often Open checks a sandbox that is still
	// starting. Default one second.
	PollInterval time.Duration
}

// New validates the configuration.
func New(cfg Config) (kritrim.Sandbox, error) {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("DAYTONA_API_KEY")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("daytona: APIKey or DAYTONA_API_KEY is required")
	}
	if cfg.APIURL == "" {
		cfg.APIURL = "https://app.daytona.io/api"
	}
	if cfg.ToolboxURL == "" {
		cfg.ToolboxURL = "https://proxy.app.daytona.io/toolbox"
	}
	if cfg.AutoStop <= 0 {
		cfg.AutoStop = 15 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	headers := map[string]string{"Authorization": "Bearer " + cfg.APIKey}
	if cfg.Organization != "" {
		headers["X-Daytona-Organization-ID"] = cfg.Organization
	}
	return &sandbox{cfg: cfg, api: boxutil.NewClient("daytona", cfg.Client, headers)}, nil
}

type sandbox struct {
	cfg Config
	api *boxutil.Client
}

type sandboxInfo struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	ErrorReason     string `json:"errorReason"`
	ToolboxProxyURL string `json:"toolboxProxyUrl"`
}

func (s *sandbox) Open(ctx context.Context, opts kritrim.OpenConfig) (kritrim.Machine, error) {
	env := map[string]string{}
	for _, kv := range opts.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	body := map[string]any{
		"env":                env,
		"networkBlockAll":    !opts.Network,
		"autoStopInterval":   int(s.cfg.AutoStop.Minutes()),
		"autoDeleteInterval": 0,
	}
	if snap := firstNonEmpty(opts.Image, s.cfg.Snapshot); snap != "" {
		body["snapshot"] = snap
	}
	var info sandboxInfo
	if err := s.api.JSON(ctx, http.MethodPost, boxutil.Join(s.cfg.APIURL, "sandbox"), body, &info, nil); err != nil {
		return nil, fmt.Errorf("daytona: create sandbox: %w", err)
	}
	if info.ID == "" {
		return nil, errors.New("daytona: create sandbox: no sandbox id")
	}
	m := &machine{s: s, id: info.ID}
	for info.State != "started" {
		switch info.State {
		case "error", "build_failed", "destroyed", "destroying":
			_ = m.Close(context.WithoutCancel(ctx))
			return nil, fmt.Errorf("daytona: sandbox %s is %s: %s", info.ID, info.State, info.ErrorReason)
		}
		select {
		case <-ctx.Done():
			_ = m.Close(context.WithoutCancel(ctx))
			return nil, ctx.Err()
		case <-time.After(s.cfg.PollInterval):
		}
		proxy := info.ToolboxProxyURL
		if err := s.api.JSON(ctx, http.MethodGet, boxutil.Join(s.cfg.APIURL, "sandbox", m.id), nil, &info, nil); err != nil {
			_ = m.Close(context.WithoutCancel(ctx))
			return nil, fmt.Errorf("daytona: sandbox status: %w", err)
		}
		if info.ToolboxProxyURL == "" {
			info.ToolboxProxyURL = proxy
		}
	}
	m.toolbox = boxutil.Join(firstNonEmpty(info.ToolboxProxyURL, s.cfg.ToolboxURL), m.id)
	return m, nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

type machine struct {
	s       *sandbox
	id      string
	toolbox string

	mu     sync.Mutex
	closed bool
}

func (m *machine) Exec(ctx context.Context, r kritrim.ExecRequest) (kritrim.ExecResult, error) {
	if len(r.Command) == 0 {
		return kritrim.ExecResult{}, errors.New("daytona: empty command")
	}
	command := boxutil.Quote(r.Command)
	if r.Stdin != nil {
		// The toolbox has no stdin, so the input goes to a file the
		// command reads and then removes.
		in := "/tmp/.kritrim-stdin-" + fmt.Sprint(time.Now().UnixNano())
		if err := m.upload(ctx, in, r.Stdin); err != nil {
			return kritrim.ExecResult{}, err
		}
		command = "sh -c " + boxutil.QuoteArg(command+" < "+boxutil.QuoteArg(in)+"; s=$?; rm -f "+boxutil.QuoteArg(in)+"; exit $s")
	}
	env := map[string]string{}
	for _, kv := range r.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	timeout := 0
	if r.Timeout > 0 {
		timeout = max(1, int(r.Timeout.Seconds()))
	}
	body := map[string]any{"command": command, "timeout": timeout, "env": env}
	if r.Dir != "" {
		body["cwd"] = r.Dir
	}
	var out struct {
		ExitCode int    `json:"exitCode"`
		Result   string `json:"result"`
	}
	if err := m.s.api.JSON(ctx, http.MethodPost, m.toolbox+"/process/execute", body, &out, nil); err != nil {
		return kritrim.ExecResult{}, fmt.Errorf("daytona: execute: %w", err)
	}
	return kritrim.ExecResult{Stdout: out.Result, Exit: out.ExitCode}, nil
}

// Start is not supported: the Daytona toolbox cannot stream a process's
// stdin and stdout.
func (m *machine) Start(ctx context.Context, r kritrim.StartRequest) (kritrim.Process, error) {
	return nil, fmt.Errorf("daytona: long-lived processes with stdio: %w", errors.ErrUnsupported)
}

func (m *machine) Read(ctx context.Context, path string) ([]byte, error) {
	resp, err := m.s.api.Do(ctx, http.MethodGet, m.toolbox+"/files/download?"+url.Values{"path": {path}}.Encode(), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("daytona: read %s: %w", path, err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (m *machine) upload(ctx context.Context, path string, data []byte) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", path)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	resp, err := m.s.api.Do(ctx, http.MethodPost, m.toolbox+"/files/upload?"+url.Values{"path": {path}}.Encode(), &body,
		map[string]string{"Content-Type": mw.FormDataContentType()})
	if err != nil {
		return fmt.Errorf("daytona: write %s: %w", path, err)
	}
	return resp.Body.Close()
}

func (m *machine) Write(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	if err := m.upload(ctx, path, data); err != nil {
		return err
	}
	res, err := m.Exec(ctx, kritrim.ExecRequest{Command: []string{"chmod", fmt.Sprintf("%o", mode.Perm()), path}, Timeout: time.Minute})
	if err != nil || res.Exit != 0 {
		return fmt.Errorf("daytona: chmod %s: %s %v", path, res.Stdout, err)
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
	err := m.s.api.JSON(ctx, http.MethodDelete, boxutil.Join(m.s.cfg.APIURL, "sandbox", m.id)+"?force=true", nil, nil, nil)
	var se *boxutil.StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return nil
	}
	return err
}
