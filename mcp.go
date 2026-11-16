package kritrim

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
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ganeshdipdumbare/kritrim/internal/boxutil"
)

// MCPConfig connects one MCP server. Set Command for stdio, or URL for
// Streamable HTTP.
type MCPConfig struct {
	Name    string
	Command string
	Args    []string
	Env     []string
	URL     string
	Headers map[string]string
	// AllowHosts is the HTTP egress allowlist. Default: the URL host.
	AllowHosts []string
	// Risky marks every tool from this server as needing approval.
	Risky bool
	// Timeout bounds each MCP call. Default 60s.
	Timeout time.Duration
}

// mcpClient is one connected server.
type mcpClient struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	peer    *mcpPeer
	http    *http.Client
	url     string
	headers map[string]string // values may be secret:// references, resolved per request
	secret  func(ctx context.Context, name string) (string, error)
	smu     sync.Mutex // guards session: tool calls run in parallel
	session string
	risky   bool
	timeout time.Duration
	close   sync.Once
}

// mcpMaxBody caps what one MCP response may hold. The server is an untrusted
// peer, so its reply must not size host memory.
const mcpMaxBody = 8 << 20

type mcpMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type mcpPeer struct {
	enc     *json.Encoder
	mu      sync.Mutex
	next    int64
	pmu     sync.Mutex
	pending map[int64]chan mcpMsg
	closed  chan struct{}
	once    sync.Once
}

func newMCPPeer(r io.Reader, w io.Writer) *mcpPeer {
	p := &mcpPeer{enc: json.NewEncoder(w), pending: map[int64]chan mcpMsg{}, closed: make(chan struct{})}
	go func() {
		dec := json.NewDecoder(r)
		for {
			var m mcpMsg
			if err := dec.Decode(&m); err != nil {
				p.once.Do(func() { close(p.closed) })
				return
			}
			if m.ID == 0 {
				continue
			}
			p.pmu.Lock()
			ch := p.pending[m.ID]
			delete(p.pending, m.ID)
			p.pmu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}()
	return p
}

func (p *mcpPeer) call(ctx context.Context, method string, params, out any) error {
	id := atomic.AddInt64(&p.next, 1)
	ch := make(chan mcpMsg, 1)
	p.pmu.Lock()
	p.pending[id] = ch
	p.pmu.Unlock()
	// A reply that arrives after the caller gave up has nobody to deliver
	// to. Drop the entry on every return path, so timeouts cannot leak it.
	defer func() {
		p.pmu.Lock()
		delete(p.pending, id)
		p.pmu.Unlock()
	}()
	b, _ := json.Marshal(params)
	p.mu.Lock()
	err := p.enc.Encode(mcpMsg{JSONRPC: "2.0", ID: id, Method: method, Params: b})
	p.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.closed:
		return errors.New("mcp: connection closed")
	case m := <-ch:
		if m.Error != nil {
			return fmt.Errorf("mcp: %s", m.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	}
}

func (p *mcpPeer) notify(method string, params any) {
	b, _ := json.Marshal(params)
	p.mu.Lock()
	_ = p.enc.Encode(mcpMsg{JSONRPC: "2.0", Method: method, Params: b})
	p.mu.Unlock()
}

func connectMCP(ctx context.Context, cfg MCPConfig, a *Agent) (*mcpClient, error) {
	if cfg.Name == "" {
		return nil, errors.New("kritrim: MCP server needs a name")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	// Copy the headers: the caller's map is theirs, and must never hold
	// resolved secrets or be written to from several agents at once.
	headers := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	c := &mcpClient{name: cfg.Name, headers: headers, risky: cfg.Risky, timeout: cfg.Timeout, secret: a.env.Secret}
	if cfg.Command != "" {
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = childEnv(cfg.Env)
		cmd.Stderr = boxutil.SafeStderr()
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("kritrim: mcp %s: %w", cfg.Name, err)
		}
		c.cmd, c.stdin, c.peer = cmd, in, newMCPPeer(out, in)
	} else if cfg.URL != "" {
		u, err := url.Parse(cfg.URL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("kritrim: mcp %s: invalid URL", cfg.Name)
		}
		hosts := cfg.AllowHosts
		if len(hosts) == 0 {
			hosts = []string{u.Hostname()}
		}
		if exact, ok := hostAllowed(u.Hostname(), hosts); !ok {
			return nil, fmt.Errorf("kritrim: mcp %s: host %q is not allowed", cfg.Name, u.Hostname())
		} else if !exact {
			if err := checkPublic(ctx, u.Hostname()); err != nil {
				return nil, err
			}
		}
		// Check the secrets exist now, so a missing one fails at connect. The
		// values are read again for each request, so a rotated secret is used.
		for _, v := range headers {
			if strings.HasPrefix(v, "secret://") {
				if _, err := a.env.Secret(ctx, strings.TrimPrefix(v, "secret://")); err != nil {
					return nil, err
				}
			}
		}
		c.url = cfg.URL
		hc, err := egressClient(&http.Client{Timeout: cfg.Timeout}, hosts)
		if err != nil {
			return nil, err
		}
		c.http = hc
		origin := strings.ToLower(u.Host)
		c.http.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			// Headers may hold secrets: never follow a redirect off the host
			// or from https to http.
			if len(via) >= 5 || strings.ToLower(req.URL.Host) != origin || downgrade(via[0].URL, req.URL) {
				return http.ErrUseLastResponse
			}
			return nil
		}
	} else {
		return nil, fmt.Errorf("kritrim: mcp %s needs Command or URL", cfg.Name)
	}

	callCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	var initResult map[string]any
	if err := c.call(callCtx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "kritrim", "version": "0.3.0"},
	}, &initResult); err != nil {
		c.Close()
		return nil, fmt.Errorf("kritrim: mcp %s: %w", cfg.Name, err)
	}
	c.notify("notifications/initialized", map[string]any{})
	return c, nil
}

func (c *mcpClient) notify(method string, params any) {
	if c.peer != nil {
		c.peer.notify(method, params)
	}
}

func (c *mcpClient) call(ctx context.Context, method string, params, out any) error {
	if c.peer != nil {
		return c.peer.call(ctx, method, params, out)
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": atomic.AddInt64(&mcpSeq, 1), "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.headers {
		if strings.HasPrefix(v, "secret://") && c.secret != nil {
			val, err := c.secret(ctx, strings.TrimPrefix(v, "secret://"))
			if err != nil {
				return err
			}
			v = val
		}
		req.Header.Set(k, v)
	}
	c.smu.Lock()
	session := c.session
	c.smu.Unlock()
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.smu.Lock()
		c.session = sid
		c.smu.Unlock()
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("mcp: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	// Streamable HTTP may answer with SSE. Take the first data frame.
	ct := resp.Header.Get("Content-Type")
	var msg mcpMsg
	if strings.HasPrefix(ct, "text/event-stream") {
		// A frame longer than the cap is an error, not a silent cut.
		sc := bufio.NewScanner(io.LimitReader(resp.Body, mcpMaxBody))
		sc.Buffer(make([]byte, 0, 64<<10), mcpMaxBody)
		found := false
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "data:") {
				if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &msg); err == nil {
					found = true
					break
				}
			}
		}
		if err := sc.Err(); err != nil && !found {
			return fmt.Errorf("mcp: reading event stream: %w", err)
		}
		if !found {
			return errors.New("mcp: no data frame in event stream")
		}
	} else {
		body, err := io.ReadAll(io.LimitReader(resp.Body, mcpMaxBody+1))
		if err != nil {
			return err
		}
		if len(body) > mcpMaxBody {
			return fmt.Errorf("mcp: response larger than %d bytes", mcpMaxBody)
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			return err
		}
	}
	if msg.Error != nil {
		return fmt.Errorf("mcp: %s", msg.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(msg.Result, out)
	}
	return nil
}

var mcpSeq int64

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (c *mcpClient) tools(ctx context.Context) ([]Tool, error) {
	var res struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := c.call(ctx, "tools/list", map[string]any{}, &res); err != nil {
		return nil, err
	}
	out := make([]Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		schema := t.InputSchema
		if schema == nil {
			schema = objectSchema(map[string]any{})
		}
		name := c.name + "__" + t.Name
		tool := Tool{Name: name, Description: t.Description, Schema: schema}
		mcpName := t.Name
		tool.run = func(ctx context.Context, args map[string]any) (string, error) {
			callCtx, cancel := context.WithTimeout(ctx, c.timeout)
			defer cancel()
			var out struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			}
			if err := c.call(callCtx, "tools/call", map[string]any{"name": mcpName, "arguments": args}, &out); err != nil {
				return "", err
			}
			var b strings.Builder
			for _, part := range out.Content {
				if part.Type == "text" {
					if b.Len()+len(part.Text) > mcpMaxBody {
						b.WriteString("\n[output truncated]")
						break
					}
					b.WriteString(part.Text)
				}
			}
			if out.IsError {
				return "", errors.New(b.String())
			}
			return b.String(), nil
		}
		if c.risky {
			tool = Risky(tool)
		}
		out = append(out, tool)
	}
	return out, nil
}

// Close stops a stdio server or releases the HTTP client.
func (c *mcpClient) Close() {
	c.close.Do(func() {
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
			_ = c.cmd.Wait()
		}
	})
}
