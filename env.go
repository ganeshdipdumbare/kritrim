package kritrim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// env is the one way tools reach the outside world. The host implements it
// directly; a sealed box implements it over RPC to the host.
type env interface {
	HTTP(ctx context.Context, req httpRequest) (*httpResponse, error)
	Exec(ctx context.Context, req execRequest) (execResult, error)
	ReadFile(ctx context.Context, req fsRequest) ([]byte, error)
	WriteFile(ctx context.Context, req fsRequest) error
	Secret(ctx context.Context, name string) (string, error)
}

type httpRequest struct {
	Method     string
	URL        string
	Headers    map[string]string
	Body       []byte
	AllowHosts []string
	Timeout    time.Duration
}

type httpResponse struct {
	Status int
	Header map[string][]string
	Body   []byte
}

type execRequest struct {
	Command []string
	Dir     string
	Env     []string
	Stdin   []byte
	Timeout time.Duration
	Allow   []string // allowed command words; empty = deny on the host
}

type execResult struct {
	Stdout string
	Stderr string
	Exit   int
}

type fsRequest struct {
	Path     string
	Root     string
	Data     []byte
	Mode     fs.FileMode
	ReadOnly bool
}

// ---- host env ----

type hostEnv struct{ a *Agent }

func newHostEnv(a *Agent) env { return &hostEnv{a: a} }

func (e *hostEnv) HTTP(ctx context.Context, r httpRequest) (*httpResponse, error) {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("kritrim: invalid URL %q", r.URL)
	}
	exact, ok := hostAllowed(u.Hostname(), r.AllowHosts)
	if !ok {
		return nil, fmt.Errorf("kritrim: host %q is not allowed", u.Hostname())
	}
	if !exact {
		if err := checkPublic(u.Hostname()); err != nil {
			return nil, err
		}
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(cctx, method, u.String(), bytes.NewReader(r.Body))
	if err != nil {
		return nil, err
	}
	for k, v := range r.Headers {
		req.Header.Set(k, e.secrets(ctx, v))
	}
	client := *e.a.cfg.httpClient()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		exact, ok := hostAllowed(req.URL.Hostname(), r.AllowHosts)
		if !ok {
			return fmt.Errorf("kritrim: redirect to %q is not allowed", req.URL.Hostname())
		}
		if !exact {
			return checkPublic(req.URL.Hostname())
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return &httpResponse{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

// httpClient returns the configured client or a default one.
func (c Config) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

func (e *hostEnv) secrets(ctx context.Context, s string) string {
	if strings.HasPrefix(s, "secret://") {
		if v, err := e.Secret(ctx, strings.TrimPrefix(s, "secret://")); err == nil {
			return v
		}
	}
	return s
}

func (e *hostEnv) Exec(ctx context.Context, r execRequest) (execResult, error) {
	if e.a.machine != nil {
		res, err := e.a.machine.Exec(ctx, ExecRequest{Command: r.Command, Dir: r.Dir, Env: r.Env,
			Stdin: r.Stdin, Timeout: r.Timeout})
		return execResult{Stdout: res.Stdout, Stderr: res.Stderr, Exit: res.Exit}, err
	}
	return localExec(ctx, r, true)
}

func localExec(ctx context.Context, r execRequest, check bool) (execResult, error) {
	if check {
		if len(r.Allow) == 0 {
			return execResult{}, errors.New("kritrim: exec denied: no allowed commands configured")
		}
		if len(r.Command) == 0 {
			return execResult{}, errors.New("kritrim: empty command")
		}
		if !commandAllowed(r.Command[0], r.Allow) {
			return execResult{}, fmt.Errorf("kritrim: command %q is not allowed", r.Command[0])
		}
	}
	if len(r.Command) == 0 {
		return execResult{}, errors.New("kritrim: empty command")
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, r.Command[0], r.Command[1:]...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), r.Env...)
	cmd.Stdin = strings.NewReader(string(r.Stdin))
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			return execResult{}, err
		}
	}
	return execResult{Stdout: out.String(), Stderr: errb.String(), Exit: code}, nil
}

func (e *hostEnv) ReadFile(ctx context.Context, r fsRequest) ([]byte, error) {
	if e.a.machine != nil {
		p, err := boxPath(r.Path, r.Root)
		if err != nil {
			return nil, err
		}
		return e.a.machine.Read(ctx, p)
	}
	return localRead(r)
}

func localRead(r fsRequest) ([]byte, error) {
	p, err := underRoot(r.Path, r.Root)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

func (e *hostEnv) WriteFile(ctx context.Context, r fsRequest) error {
	if e.a.machine != nil {
		if r.ReadOnly {
			return errors.New("kritrim: files tool is read-only")
		}
		p, err := boxPath(r.Path, r.Root)
		if err != nil {
			return err
		}
		return e.a.machine.Write(ctx, p, r.Data, r.Mode)
	}
	return localWrite(r)
}

func localWrite(r fsRequest) error {
	if r.ReadOnly {
		return errors.New("kritrim: files tool is read-only")
	}
	p, err := underRoot(r.Path, r.Root)
	if err != nil {
		return err
	}
	mode := r.Mode
	if mode == 0 {
		mode = 0o644
	}
	if p != r.Root {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(p, r.Data, mode)
}

func (e *hostEnv) Secret(ctx context.Context, name string) (string, error) {
	v, ok := e.a.cfg.Secrets[name]
	if !ok {
		v, ok = os.LookupEnv(name)
	}
	if !ok || v == "" {
		return "", fmt.Errorf("kritrim: secret %q is not set", name)
	}
	e.a.redactor.add(v)
	return v, nil
}

// ---- shared checks ----

// boxPath resolves a path inside a sandbox, where the host's absolute paths
// mean nothing.
func boxPath(p, root string) (string, error) {
	if root == "" {
		root = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = path.Join(root, p)
	}
	p = path.Clean(p)
	if root != "/" && p != root && !strings.HasPrefix(p, root+"/") {
		return "", fmt.Errorf("kritrim: path %q escapes %q", p, root)
	}
	if strings.HasPrefix(p, "/../") {
		return "", fmt.Errorf("kritrim: path %q escapes %q", p, root)
	}
	return p, nil
}

func underRoot(path, root string) (string, error) {
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(absRoot, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("kritrim: path %q escapes %q", path, root)
	}
	return abs, nil
}

// hostAllowed reports whether host matches rules and whether the match was
// exact (an exact host may be private).
func hostAllowed(host string, rules []string) (exact, ok bool) {
	host = strings.ToLower(host)
	for _, r := range rules {
		r = strings.ToLower(strings.TrimSpace(r))
		if r == "" {
			continue
		}
		if r == host {
			return true, true
		}
		if strings.HasPrefix(r, "*.") && strings.HasSuffix(host, r[1:]) && host != r[2:] {
			return false, true
		}
	}
	return false, false
}

func commandAllowed(name string, allow []string) bool {
	for _, a := range allow {
		if name == a {
			return true
		}
	}
	return false
}

func checkPublic(host string) error {
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("kritrim: resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			// shortcut: DNS is resolved here and again by the client, so a
			// rebinding answer can slip through; re-check at the dialer when
			// untrusted hosts are allowed.
			return fmt.Errorf("kritrim: host %q resolves to a private address", host)
		}
	}
	return nil
}
