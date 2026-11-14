// Package boxutil holds the pieces sandbox adapters share: an HTTP JSON
// client that keeps credentials on one host, a server-sent-events reader,
// shell quoting, and a Process built from callbacks.
package boxutil

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// StatusError is a non-2xx reply from a provider.
type StatusError struct {
	Provider string
	Code     int
	Body     string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.Code, strings.TrimSpace(e.Body))
}

// Client calls one provider API. Redirects are never followed, so a
// credential header cannot be carried to another host.
type Client struct {
	Provider string
	HTTP     *http.Client
	Headers  map[string]string
}

// NewClient copies base (or a default client) with redirects disabled.
func NewClient(provider string, base *http.Client, headers map[string]string) *Client {
	c := http.Client{Timeout: 5 * time.Minute}
	if base != nil {
		c = *base
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{Provider: provider, HTTP: &c, Headers: headers}
}

// Do sends a request with the client headers plus extra, and returns the
// response for a 2xx status. The caller closes the body.
func (c *Client) Do(ctx context.Context, method, u string, body io.Reader, extra map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &StatusError{Provider: c.Provider, Code: resp.StatusCode, Body: string(b)}
	}
	return resp, nil
}

// JSON sends in as JSON (nil sends no body) and decodes the reply into out
// (nil discards it).
func (c *Client) JSON(ctx context.Context, method, u string, in, out any, extra map[string]string) error {
	var body io.Reader
	h := map[string]string{"Accept": "application/json"}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
		h["Content-Type"] = "application/json"
	}
	for k, v := range extra {
		h[k] = v
	}
	resp, err := c.Do(ctx, method, u, body, h)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

// Join appends path segments to base, escaping each one.
func Join(base string, segments ...string) string {
	out := strings.TrimSuffix(base, "/")
	for _, s := range segments {
		out += "/" + url.PathEscape(s)
	}
	return out
}

// SSE calls fn with the data of each server-sent event until the stream
// ends or fn returns false.
func SSE(r io.Reader, fn func(data []byte) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	var data []byte
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			if len(data) > 0 {
				if !fn(data) {
					return nil
				}
				data = nil
			}
			continue
		}
		if v, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, bytes.TrimPrefix(v, []byte(" "))...)
		}
	}
	if len(data) > 0 {
		fn(data)
	}
	return sc.Err()
}

// Quote quotes argv for a POSIX shell.
func Quote(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = QuoteArg(a)
	}
	return strings.Join(parts, " ")
}

// QuoteArg quotes one argument for a POSIX shell.
func QuoteArg(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@%+,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Process is a kritrim.Process driven by callbacks: writes to Stdin call
// Send, closing Stdin calls CloseStdin, the adapter feeds output with
// Output and finishes with Exit.
type Process struct {
	Send       func(p []byte) error
	CloseStdin func() error
	KillFunc   func() error

	once   sync.Once
	pr     *io.PipeReader
	pw     *io.PipeWriter
	done   chan struct{}
	exitMu sync.Mutex
	err    error
}

// NewProcess returns a process with its output pipe ready.
func NewProcess(send func([]byte) error, closeStdin, kill func() error) *Process {
	pr, pw := io.Pipe()
	return &Process{Send: send, CloseStdin: closeStdin, KillFunc: kill, pr: pr, pw: pw, done: make(chan struct{})}
}

// Output appends bytes to the process stdout.
func (p *Process) Output(b []byte) error {
	_, err := p.pw.Write(b)
	return err
}

// Exit ends the process: stdout reaches EOF and Wait returns err.
func (p *Process) Exit(err error) {
	p.once.Do(func() {
		p.exitMu.Lock()
		p.err = err
		p.exitMu.Unlock()
		_ = p.pw.Close()
		close(p.done)
	})
}

func (p *Process) Stdin() io.WriteCloser { return stdinWriter{p} }
func (p *Process) Stdout() io.ReadCloser { return p.pr }

// Wait blocks until Exit.
func (p *Process) Wait() error {
	<-p.done
	p.exitMu.Lock()
	defer p.exitMu.Unlock()
	return p.err
}

// Kill stops the process.
func (p *Process) Kill() error {
	var err error
	if p.KillFunc != nil {
		err = p.KillFunc()
	}
	p.Exit(errors.New("killed"))
	return err
}

type stdinWriter struct{ p *Process }

func (w stdinWriter) Write(b []byte) (int, error) {
	if w.p.Send == nil {
		return 0, errors.New("stdin is not available")
	}
	if err := w.p.Send(append([]byte(nil), b...)); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (w stdinWriter) Close() error {
	if w.p.CloseStdin == nil {
		return nil
	}
	return w.p.CloseStdin()
}

// ExitError is the error Wait returns for a non-zero exit.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }
