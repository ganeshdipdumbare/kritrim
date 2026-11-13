package a2a

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// ClientConfig configures a remote A2A agent.
type ClientConfig struct {
	Endpoint string
	// CardPath defaults to /.well-known/agent-card.json.
	CardPath string
	Headers  map[string]string
	// AllowHosts is the egress allowlist for the endpoint and its card.
	// Default: the endpoint host.
	AllowHosts []string
	Client     *http.Client
	Timeout    time.Duration

	// OAuth client-credentials grant. When TokenURL is set, the client
	// fetches and refreshes a bearer token for every request.
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string

	// PushURL registers a webhook for every task this client creates.
	PushURL   string
	PushToken string
}

// Client calls a remote A2A agent.
type Client struct {
	base   *url.URL
	cfg    ClientConfig
	http   *http.Client
	card   *Card
	cardAt time.Time

	tokenMu  sync.Mutex
	token    string
	tokenExp time.Time
}

// NewClient validates the endpoint and returns a client.
func NewClient(cfg ClientConfig) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("a2a: invalid endpoint %q", cfg.Endpoint)
	}
	if cfg.CardPath == "" {
		cfg.CardPath = CardPath
	}
	if len(cfg.AllowHosts) == 0 {
		cfg.AllowHosts = []string{u.Hostname()}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: cfg.Timeout}
	}
	if !allowed(u.Hostname(), cfg.AllowHosts) {
		return nil, fmt.Errorf("a2a: host %q is not allowed", u.Hostname())
	}
	// Requests carry credentials, so a redirect must stay on the same host;
	// anything else is returned to the caller instead of followed.
	hc := *cfg.Client
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !strings.EqualFold(req.URL.Host, via[0].URL.Host) ||
			!allowed(req.URL.Hostname(), cfg.AllowHosts) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return &Client{base: u, cfg: cfg, http: &hc}, nil
}

func allowed(host string, rules []string) bool {
	host = strings.ToLower(host)
	for _, r := range rules {
		r = strings.ToLower(strings.TrimSpace(r))
		if r == host || (strings.HasPrefix(r, "*.") && strings.HasSuffix(host, r[1:])) {
			return true
		}
	}
	return false
}

func (c *Client) headers(ctx context.Context, req *http.Request) error {
	for k, v := range c.cfg.Headers {
		if strings.HasPrefix(v, "secret://") {
			v = os.Getenv(strings.TrimPrefix(v, "secret://"))
		}
		req.Header.Set(k, v)
	}
	if c.cfg.TokenURL == "" || req.Header.Get("Authorization") != "" {
		return nil
	}
	tok, err := c.bearer(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

func (c *Client) bearer(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", c.cfg.ClientSecret)
	if len(c.cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(c.cfg.Scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("a2a: token: %s", resp.Status)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", errors.New("a2a: token response has no access_token")
	}
	c.token = tr.AccessToken
	c.tokenExp = time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second - 30*time.Second)
	return c.token, nil
}

// Card fetches and caches the remote agent card.
func (c *Client) Card(ctx context.Context) (*Card, error) {
	if c.card != nil && time.Since(c.cardAt) < 10*time.Minute {
		return c.card, nil
	}
	u := *c.base
	u.Path = strings.TrimSuffix(u.Path, "/") + c.cfg.CardPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if err := c.headers(ctx, req); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("a2a: card: %s", resp.Status)
	}
	var card Card
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&card); err != nil {
		return nil, err
	}
	c.card, c.cardAt = &card, time.Now()
	return &card, nil
}

// Run sends a message and waits for the task to finish. When decisions are
// supplied it resumes the task instead of sending a new prompt.
func (c *Client) Run(ctx context.Context, prompt string, opts ...RunOption) (*kritrim.Result, error) {
	o := resolveRun(opts)
	msg := c.message(prompt, o)
	params := map[string]any{"message": msg}
	if c.cfg.PushURL != "" {
		params["configuration"] = map[string]any{"pushNotificationConfig": PushConfig{URL: c.cfg.PushURL, Token: c.cfg.PushToken}}
	}
	var raw json.RawMessage
	if err := c.call(ctx, "message/send", params, &raw); err != nil {
		return nil, err
	}
	task, err := decodeTask(raw)
	if err != nil {
		return nil, err
	}
	return resultOf(task)
}

// Stream sends a message and yields text and lifecycle events. Use Run when
// you need the final task and interrupt state.
func (c *Client) Stream(ctx context.Context, prompt string, opts ...RunOption) iter.Seq2[kritrim.Event, error] {
	return func(yield func(kritrim.Event, error) bool) {
		o := resolveRun(opts)
		params := map[string]any{"message": c.message(prompt, o)}
		if c.cfg.PushURL != "" {
			params["configuration"] = map[string]any{"pushNotificationConfig": PushConfig{URL: c.cfg.PushURL, Token: c.cfg.PushToken}}
		}
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "message/stream",
			"params": params,
		})
		u := *c.base
		u.Path = strings.TrimSuffix(u.Path, "/") + "/"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
		if err != nil {
			yield(kritrim.Event{Type: "error", Err: err}, err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if err := c.headers(ctx, req); err != nil {
			yield(kritrim.Event{Type: "error", Err: err}, err)
			return
		}
		resp, err := c.http.Do(req)
		if err != nil {
			yield(kritrim.Event{Type: "error", Err: err}, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			yield(kritrim.Event{Type: "error", Err: fmt.Errorf("a2a: stream: %s", resp.Status)}, fmt.Errorf("a2a: stream: %s", resp.Status))
			return
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var ev StreamEvent
			if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev); err != nil {
				continue
			}
			if ev.Artifact != nil {
				for _, p := range ev.Artifact.Parts {
					if p.Kind == "text" && p.Text != "" && !yield(kritrim.Event{Type: "text", Text: p.Text}, nil) {
						return
					}
				}
			}
			if ev.Status != nil && ev.Final {
				t := "finish"
				if ev.Status.State == StateInputRequired {
					t = "interrupt"
				}
				if !yield(kritrim.Event{Type: t, Text: ev.Status.State}, nil) {
					return
				}
			}
		}
		if err := sc.Err(); err != nil {
			yield(kritrim.Event{Type: "error", Err: err}, err)
		}
	}
}

// AsTool exposes the remote agent as a tool.
func (c *Client) AsTool(name, description string) kritrim.Tool {
	return kritrim.NewTool(name, description, func(ctx context.Context, in struct {
		Task string `json:"task" desc:"the task, with all context it needs"`
	}) (string, error) {
		res, err := c.Run(ctx, in.Task)
		if err != nil {
			return "", err
		}
		if res.Stop == "interrupt" {
			return "", errors.New("remote agent needs approval; ask the user")
		}
		return res.Text, nil
	})
}

// RunOption configures one client run.
type RunOption func(*runConfig)

type runConfig struct {
	contextID string
	taskID    string
	decisions map[string]kritrim.Decision
}

// ContextID pins the A2A context (conversation) id.
func ContextID(id string) RunOption { return func(o *runConfig) { o.contextID = id } }

// TaskID resumes an existing task.
func TaskID(id string) RunOption { return func(o *runConfig) { o.taskID = id } }

// Decisions resumes a task paused on interrupts.
func Decisions(m map[string]kritrim.Decision) RunOption {
	return func(o *runConfig) { o.decisions = m }
}

func resolveRun(opts []RunOption) runConfig {
	var o runConfig
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

func (c *Client) message(prompt string, o runConfig) Message {
	msg := Message{Role: "user", MessageID: newID(), TaskID: o.taskID, ContextID: o.contextID}
	if len(o.decisions) > 0 {
		for id, d := range o.decisions {
			msg.Parts = append(msg.Parts, DataPart(map[string]any{
				"interruptResponse": map[string]any{"interruptId": id, "response": decisionValue(d)},
			}))
		}
		return msg
	}
	msg.Parts = []Part{TextPart(prompt)}
	return msg
}

func decisionValue(d kritrim.Decision) any {
	if d.Respond != "" {
		return map[string]any{"respond": d.Respond}
	}
	if d.Args != nil {
		return map[string]any{"approved": true, "args": d.Args}
	}
	if d.Approve {
		return true
	}
	return map[string]any{"approved": false, "reason": d.Reason}
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	u := *c.base
	u.Path = strings.TrimSuffix(u.Path, "/") + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := c.headers(ctx, req); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("a2a: %s: %s", method, resp.Status)
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rpc); err != nil {
		return err
	}
	if rpc.Error != nil {
		return fmt.Errorf("a2a: %s: %s", method, rpc.Error.Message)
	}
	return json.Unmarshal(rpc.Result, out)
}

func decodeTask(raw json.RawMessage) (*Task, error) {
	var t Task
	if err := json.Unmarshal(raw, &t); err == nil && t.ID != "" {
		return &t, nil
	}
	var w struct {
		Task Task `json:"task"`
	}
	if err := json.Unmarshal(raw, &w); err == nil && w.Task.ID != "" {
		return &w.Task, nil
	}
	return nil, errors.New("a2a: response did not contain a task")
}

func resultOf(t *Task) (*kritrim.Result, error) {
	res := &kritrim.Result{Thread: t.ContextID}
	var text strings.Builder
	for _, a := range t.Artifacts {
		for _, p := range a.Parts {
			if p.Kind == "text" {
				text.WriteString(p.Text)
			}
		}
	}
	if text.Len() == 0 && t.Status.Message != nil {
		text.WriteString(t.Status.Message.Text())
	}
	res.Text = text.String()
	switch t.Status.State {
	case StateCompleted:
		res.Stop = "stop"
	case StateInputRequired:
		res.Stop = "interrupt"
		res.Interrupts = interruptsOf(t)
	case StateFailed:
		return nil, fmt.Errorf("a2a: task failed: %s", res.Text)
	case StateCanceled:
		res.Stop = "stop"
	default:
		res.Stop = "stop"
	}
	return res, nil
}

func interruptsOf(t *Task) []kritrim.Interrupt {
	if t.Status.Message == nil {
		return nil
	}
	var out []kritrim.Interrupt
	for _, p := range t.Status.Message.Parts {
		if p.Kind != "data" {
			continue
		}
		raw, ok := p.Data["interrupts"].([]any)
		if !ok {
			continue
		}
		for _, x := range raw {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			id, _ := m["interruptId"].(string)
			name, _ := m["name"].(string)
			args, _ := m["reason"].(map[string]any)
			out = append(out, kritrim.Interrupt{ID: id, Name: name, Tool: name, CallID: id, Args: args})
		}
	}
	return out
}

// StreamEvent is one A2A streaming event.
type StreamEvent struct {
	Kind      string      `json:"kind"`
	TaskID    string      `json:"taskId"`
	ContextID string      `json:"contextId"`
	Status    *TaskStatus `json:"status,omitempty"`
	Artifact  *Artifact   `json:"artifact,omitempty"`
	Final     bool        `json:"final"`
}
