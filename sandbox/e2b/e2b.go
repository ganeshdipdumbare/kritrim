// Package e2b provides a kritrim sandbox on E2B (e2b.dev) cloud sandboxes.
//
//	box, err := e2b.New(e2b.Config{Template: "base"})
//	agent, err := kritrim.New(kritrim.Config{Model: model, Tools: tools, Sandbox: box})
//
// Each Open creates a sandbox through the E2B API and Close kills it.
// Commands, files and long-lived processes go to the sandbox's envd daemon:
// processes over its Connect RPC API with real stdin and stdout, so an E2B
// sandbox can host a sealed agent. The API key stays on the host; it is
// never sent to the sandbox.
package e2b

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
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

// Config configures E2B sandboxes.
type Config struct {
	// APIKey defaults to the E2B_API_KEY environment variable.
	APIKey string
	// Template is the sandbox template. Default "base". OpenConfig.Image
	// overrides it.
	Template string
	// Domain is the E2B domain. Default "e2b.app".
	Domain string
	// APIURL defaults to https://api.<Domain>.
	APIURL string
	// EnvdURL overrides the sandbox daemon URL, normally
	// https://49983-<sandbox id>.<domain>. Tests and self-hosted setups.
	EnvdURL string
	// Lifetime is how long E2B keeps a sandbox alive if Close is never
	// called. Default one hour.
	Lifetime time.Duration
	// User runs commands and owns files. Default "user".
	User   string
	Client *http.Client
}

const envdPort = "49983"

// New validates the configuration.
func New(cfg Config) (kritrim.Sandbox, error) {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("E2B_API_KEY")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("e2b: APIKey or E2B_API_KEY is required")
	}
	if cfg.Template == "" {
		cfg.Template = "base"
	}
	if cfg.Domain == "" {
		cfg.Domain = "e2b.app"
	}
	if cfg.APIURL == "" {
		cfg.APIURL = "https://api." + cfg.Domain
	}
	if cfg.Lifetime <= 0 {
		cfg.Lifetime = time.Hour
	}
	if cfg.User == "" {
		cfg.User = "user"
	}
	return &sandbox{cfg: cfg, api: boxutil.NewClient("e2b", cfg.Client, map[string]string{"X-API-Key": cfg.APIKey})}, nil
}

type sandbox struct {
	cfg Config
	api *boxutil.Client
}

func (s *sandbox) Open(ctx context.Context, opts kritrim.OpenConfig) (kritrim.Machine, error) {
	template := s.cfg.Template
	if opts.Image != "" {
		template = opts.Image
	}
	env := map[string]string{}
	for _, kv := range opts.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	var created struct {
		SandboxID       string `json:"sandboxID"`
		EnvdAccessToken string `json:"envdAccessToken"`
		Domain          string `json:"domain"`
	}
	err := s.api.JSON(ctx, http.MethodPost, boxutil.Join(s.cfg.APIURL, "sandboxes"), map[string]any{
		"templateID":            template,
		"timeout":               int(s.cfg.Lifetime.Seconds()),
		"allow_internet_access": opts.Network,
		"secure":                true,
		"envVars":               env,
	}, &created, nil)
	if err != nil {
		return nil, fmt.Errorf("e2b: create sandbox: %w", err)
	}
	if created.SandboxID == "" {
		return nil, errors.New("e2b: create sandbox: no sandbox id")
	}
	domain := created.Domain
	if domain == "" {
		domain = s.cfg.Domain
	}
	envd := s.cfg.EnvdURL
	if envd == "" {
		envd = "https://" + envdPort + "-" + created.SandboxID + "." + domain
	}
	headers := map[string]string{
		"E2b-Sandbox-Id":   created.SandboxID,
		"E2b-Sandbox-Port": envdPort,
		"Authorization":    "Basic " + base64.StdEncoding.EncodeToString([]byte(s.cfg.User+":")),
	}
	if created.EnvdAccessToken != "" {
		headers["X-Access-Token"] = created.EnvdAccessToken
	}
	// Processes stream for as long as they run; the context bounds them,
	// not a client timeout.
	stream := boxutil.NewClient("e2b", s.cfg.Client, headers)
	stream.HTTP.Timeout = 0
	return &machine{s: s, id: created.SandboxID, envd: strings.TrimSuffix(envd, "/"),
		files: boxutil.NewClient("e2b", s.cfg.Client, headers), stream: stream}, nil
}

type machine struct {
	s      *sandbox
	id     string
	envd   string
	files  *boxutil.Client
	stream *boxutil.Client

	mu     sync.Mutex
	closed bool
}

// event is one envd process event (protojson).
type event struct {
	Start *struct {
		PID uint32 `json:"pid"`
	} `json:"start"`
	Data *struct {
		Stdout []byte `json:"stdout"`
		Stderr []byte `json:"stderr"`
	} `json:"data"`
	End *struct {
		ExitCode int    `json:"exitCode"`
		Exited   bool   `json:"exited"`
		Status   string `json:"status"`
		Error    string `json:"error"`
	} `json:"end"`
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

// start opens the Process.Start stream and calls fn for each event.
func (m *machine) start(ctx context.Context, argv []string, dir string, env []string, stdin bool, fn func(event) error) error {
	proc := map[string]any{"cmd": argv[0], "args": argv[1:], "envs": envMap(env)}
	if dir != "" {
		proc["cwd"] = dir
	}
	msg, _ := json.Marshal(map[string]any{"process": proc, "stdin": stdin})
	var body bytes.Buffer
	writeEnvelope(&body, 0, msg)
	resp, err := m.stream.Do(ctx, http.MethodPost, m.envd+"/process.Process/Start", &body, map[string]string{
		"Content-Type":             "application/connect+json",
		"Connect-Protocol-Version": "1",
	})
	if err != nil {
		return fmt.Errorf("e2b: start: %w", err)
	}
	defer resp.Body.Close()
	for {
		flags, data, err := readEnvelope(resp.Body)
		if err == io.EOF {
			return errors.New("e2b: process stream ended without an end event")
		}
		if err != nil {
			return fmt.Errorf("e2b: process stream: %w", err)
		}
		if flags&0x02 != 0 {
			var end struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(data, &end)
			if end.Error != nil {
				return fmt.Errorf("e2b: %s: %s", end.Error.Code, end.Error.Message)
			}
			return errors.New("e2b: process stream ended without an end event")
		}
		var msg struct {
			Event event `json:"event"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("e2b: process event: %w", err)
		}
		if err := fn(msg.Event); err != nil {
			return err
		}
		if msg.Event.End != nil {
			return nil
		}
	}
}

func (m *machine) unary(ctx context.Context, method string, in any) error {
	return m.files.JSON(ctx, http.MethodPost, m.envd+"/process.Process/"+method, in, nil,
		map[string]string{"Connect-Protocol-Version": "1"})
}

func (m *machine) Exec(ctx context.Context, r kritrim.ExecRequest) (kritrim.ExecResult, error) {
	if len(r.Command) == 0 {
		return kritrim.ExecResult{}, errors.New("e2b: empty command")
	}
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	var out, errb strings.Builder
	res := kritrim.ExecResult{}
	err := m.start(ctx, r.Command, r.Dir, r.Env, r.Stdin != nil, func(e event) error {
		switch {
		case e.Start != nil && r.Stdin != nil:
			pid := e.Start.PID
			go func() {
				sel := map[string]any{"pid": pid}
				if len(r.Stdin) > 0 {
					_ = m.unary(ctx, "SendInput", map[string]any{"process": sel, "input": map[string]any{"stdin": r.Stdin}})
				}
				_ = m.unary(ctx, "CloseStdin", map[string]any{"process": sel})
			}()
		case e.Data != nil:
			out.Write(e.Data.Stdout)
			errb.Write(e.Data.Stderr)
		case e.End != nil:
			res.Exit = e.End.ExitCode
			if e.End.Error != "" && !e.End.Exited {
				return fmt.Errorf("e2b: %s", e.End.Error)
			}
		}
		return nil
	})
	res.Stdout, res.Stderr = out.String(), errb.String()
	return res, err
}

func (m *machine) Start(ctx context.Context, r kritrim.StartRequest) (kritrim.Process, error) {
	if len(r.Command) == 0 {
		return nil, errors.New("e2b: empty command")
	}
	// The process outlives ctx, which only bounds starting it.
	life, stop := context.WithCancel(context.WithoutCancel(ctx))
	ready := make(chan uint32, 1)
	var pid uint32
	sel := func() map[string]any { return map[string]any{"pid": pid} }
	p := boxutil.NewProcess(
		func(b []byte) error {
			return m.unary(life, "SendInput", map[string]any{"process": sel(), "input": map[string]any{"stdin": b}})
		},
		func() error { return m.unary(life, "CloseStdin", map[string]any{"process": sel()}) },
		func() error {
			defer stop()
			return m.unary(life, "SendSignal", map[string]any{"process": sel(), "signal": "SIGNAL_SIGKILL"})
		},
	)
	errc := make(chan error, 1)
	go func() {
		defer stop()
		err := m.start(life, r.Command, r.Dir, r.Env, true, func(e event) error {
			switch {
			case e.Start != nil:
				ready <- e.Start.PID
			case e.Data != nil:
				if len(e.Data.Stdout) > 0 {
					_ = p.Output(e.Data.Stdout)
				}
				if len(e.Data.Stderr) > 0 {
					_, _ = os.Stderr.Write(e.Data.Stderr)
				}
			case e.End != nil:
				if e.End.ExitCode != 0 {
					p.Exit(&boxutil.ExitError{Code: e.End.ExitCode})
				} else {
					p.Exit(nil)
				}
			}
			return nil
		})
		if err != nil {
			errc <- err
			p.Exit(err)
		}
	}()
	select {
	case pid = <-ready:
		return p, nil
	case err := <-errc:
		return nil, err
	case <-ctx.Done():
		stop()
		return nil, ctx.Err()
	}
}

func (m *machine) fileURL(path string) string {
	return m.envd + "/files?" + url.Values{"path": {path}, "username": {m.s.cfg.User}}.Encode()
}

func (m *machine) Read(ctx context.Context, path string) ([]byte, error) {
	resp, err := m.files.Do(ctx, http.MethodGet, m.fileURL(path), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("e2b: read %s: %w", path, err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (m *machine) Write(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
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
	resp, err := m.files.Do(ctx, http.MethodPost, m.fileURL(path), &body, map[string]string{"Content-Type": mw.FormDataContentType()})
	if err != nil {
		return fmt.Errorf("e2b: write %s: %w", path, err)
	}
	resp.Body.Close()
	res, err := m.Exec(ctx, kritrim.ExecRequest{Command: []string{"chmod", fmt.Sprintf("%o", mode.Perm()), path}, Timeout: time.Minute})
	if err != nil || res.Exit != 0 {
		return fmt.Errorf("e2b: chmod %s: %s %v", path, res.Stderr, err)
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

// writeEnvelope frames one Connect streaming message.
func writeEnvelope(w io.Writer, flags byte, msg []byte) {
	var h [5]byte
	h[0] = flags
	binary.BigEndian.PutUint32(h[1:], uint32(len(msg)))
	w.Write(h[:])
	w.Write(msg)
}

// readEnvelope reads one Connect streaming message.
func readEnvelope(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return 0, nil, io.EOF
		}
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > 64<<20 {
		return 0, nil, fmt.Errorf("message of %d bytes is too large", n)
	}
	msg := make([]byte, n)
	if _, err := io.ReadFull(r, msg); err != nil {
		return 0, nil, err
	}
	return h[0], msg, nil
}
