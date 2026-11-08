package kritrim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// runParams is what the host sends to the box to start a run.
type runParams struct {
	Prompt     string              `json:"prompt"`
	Thread     string              `json:"thread,omitempty"`
	Messages   []Message           `json:"messages,omitempty"`
	Pending    []Interrupt         `json:"pending,omitempty"`
	Paused     *PausedStep         `json:"paused,omitempty"`
	Decisions  map[string]Decision `json:"decisions,omitempty"`
	User       string              `json:"user,omitempty"`
	Roles      []string            `json:"roles,omitempty"`
	Images     []Image             `json:"images,omitempty"`
	Schema     map[string]any      `json:"schema,omitempty"`
	SchemaName string              `json:"schema_name,omitempty"`
}

type secretBinding struct {
	value string
	hosts []string
}

type boxHost struct {
	a    *Agent
	p    *peer
	m    Machine
	proc Process
	cfg  Sealed

	emu  sync.RWMutex
	emit func(Event)

	umu  sync.Mutex
	used int

	smu     sync.Mutex
	secrets map[string]secretBinding

	runMu sync.Mutex
}

func newSealed(cfg Config) (*Agent, error) {
	s := cfg.Sealed
	if s == nil || s.Sandbox == nil {
		return nil, errors.New("kritrim: Sealed needs a Sandbox")
	}
	if cfg.Model == nil {
		return nil, errors.New("kritrim: no model; set Config.Model")
	}
	if _, local := s.Sandbox.(localSandbox); local && !s.AllowUnsafeLocal {
		return nil, errors.New("kritrim: the local sandbox is not an isolation boundary; set Sealed.AllowUnsafeLocal for development")
	}
	seen := map[string]bool{}
	for _, t := range cfg.Tools {
		if err := t.validate(); err != nil {
			return nil, err
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("kritrim: duplicate tool %q", t.Name)
		}
		seen[t.Name] = true
	}
	if cfg.Limits.Steps == 0 {
		cfg.Limits.Steps = 20
	}
	if cfg.Limits.ToolCalls == 0 {
		cfg.Limits.ToolCalls = 100
	}
	if cfg.Session == nil {
		cfg.Session = MemorySessions()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	m, err := s.Sandbox.Open(ctx, OpenConfig{})
	if err != nil {
		return nil, fmt.Errorf("kritrim: open sandbox: %w", err)
	}
	bin := s.Binary
	if bin == "" {
		if _, local := s.Sandbox.(localSandbox); !local {
			target := s.Platform
			if target == "" {
				target = "linux/amd64"
			}
			parts := strings.SplitN(target, "/", 2)
			if len(parts) != 2 {
				parts = []string{"linux", "amd64"}
			}
			_ = m.Close(context.Background())
			return nil, fmt.Errorf("kritrim: this sealed sandbox needs a binary built for %s; set Sealed.Binary or run: GOOS=%s GOARCH=%s go build -o box <package>", target, parts[0], parts[1])
		}
		bin, err = os.Executable()
		if err != nil {
			_ = m.Close(ctx)
			return nil, err
		}
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		_ = m.Close(ctx)
		return nil, fmt.Errorf("kritrim: read box binary: %w", err)
	}
	const boxBin = "/tmp/kritrim-agent"
	if err := m.Write(ctx, boxBin, data, 0o755); err != nil {
		_ = m.Close(ctx)
		return nil, fmt.Errorf("kritrim: upload box binary: %w", err)
	}
	proc, err := m.Start(ctx, StartRequest{Command: []string{boxBin}, Env: []string{"KRITRIM_BOX=1"}})
	if err != nil {
		_ = m.Close(ctx)
		return nil, fmt.Errorf("kritrim: start box: %w", err)
	}
	a := &Agent{cfg: cfg, model: cfg.Model, session: cfg.Session, redactor: &redactor{}}
	a.cfg.Tools = append([]Tool(nil), cfg.Tools...)
	a.env = newHostEnv(a)
	if err := a.connectMCP(ctx, cfg.MCP); err != nil {
		_ = m.Close(context.Background())
		return nil, err
	}
	b := &boxHost{a: a, p: newPeer(proc.Stdout(), proc.Stdin()), m: m, proc: proc, cfg: *s,
		secrets: map[string]secretBinding{}}
	b.cfg.Bridged = append([]string(nil), b.cfg.Bridged...)
	b.cfg.Bridged = append(b.cfg.Bridged, a.mcpTools...)
	b.register()
	if err := b.p.call(ctx, "ping", nil, nil); err != nil {
		_ = proc.Kill()
		_ = m.Close(context.Background())
		return nil, fmt.Errorf("kritrim: box did not start: %w", err)
	}
	if err := b.syncTools(ctx); err != nil {
		_ = proc.Kill()
		_ = m.Close(context.Background())
		return nil, err
	}
	a.box = b
	return a, nil
}

// syncTools sends the definitions of bridged host tools to the box.
func (b *boxHost) syncTools(ctx context.Context) error {
	var defs []Tool
	for _, name := range b.cfg.Bridged {
		if t, ok := b.a.tool(name); ok {
			defs = append(defs, t)
		}
	}
	if len(defs) == 0 {
		return nil
	}
	return b.p.call(ctx, "tools.add", defs, nil)
}

func (b *boxHost) register() {
	b.p.handle("model", b.model)
	b.p.handle("http", b.http)
	b.p.handle("secret", b.secret)
	b.p.handle("tool", b.tool)
	b.p.handle("approve", b.approve)
	b.p.handle("audit", b.audit)
	b.p.handle("event", func(_ context.Context, raw json.RawMessage) (any, error) {
		var e Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		b.emu.RLock()
		emit := b.emit
		b.emu.RUnlock()
		if emit != nil {
			emit(e)
		}
		return nil, nil
	})
	b.p.handle("ping", func(context.Context, json.RawMessage) (any, error) { return map[string]bool{"ok": true}, nil })
}

func (b *boxHost) model(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Call    string `json:"call"`
		Request Request
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	p.Request.Model = b.a.Model()
	resp, err := b.a.model.Stream(ctx, p.Request, func(d Delta) {
		b.p.notify("model.delta", map[string]any{"call": p.Call, "text": d.Text, "reasoning": d.Reasoning})
	})
	if err != nil {
		return nil, err
	}
	b.umu.Lock()
	b.used += resp.Usage.Total()
	over := b.cfg.MaxTokens > 0 && b.used > b.cfg.MaxTokens
	used := b.used
	b.umu.Unlock()
	if over {
		return nil, fmt.Errorf("kritrim: sealed token budget %d exceeded (%d used)", b.cfg.MaxTokens, used)
	}
	return resp, nil
}

func (b *boxHost) http(ctx context.Context, raw json.RawMessage) (any, error) {
	var r httpRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	allow := append([]string{}, b.cfg.Egress...)
	for _, hosts := range b.cfg.Secrets {
		allow = append(allow, hosts...)
	}
	r.AllowHosts = allow
	if err := b.substitute(&r); err != nil {
		return nil, err
	}
	resp, err := b.a.env.HTTP(ctx, r)
	if err != nil {
		return nil, err
	}
	resp.Body = []byte(b.a.redactor.redact(string(resp.Body)))
	for k, vs := range resp.Header {
		for i, v := range vs {
			resp.Header[k][i] = b.a.redactor.redact(v)
		}
	}
	return resp, nil
}

func (b *boxHost) substitute(r *httpRequest) error {
	u, err := url.Parse(r.URL)
	if err != nil {
		return err
	}
	host := strings.ToLower(u.Hostname())
	b.smu.Lock()
	defer b.smu.Unlock()
	for ph, bind := range b.secrets {
		if !hostListed(host, bind.hosts) {
			continue
		}
		r.URL = strings.ReplaceAll(r.URL, ph, bind.value)
		for k, v := range r.Headers {
			r.Headers[k] = strings.ReplaceAll(v, ph, bind.value)
		}
		r.Body = bytes.ReplaceAll(r.Body, []byte(ph), []byte(bind.value))
	}
	for ph := range b.secrets {
		if strings.Contains(r.URL, ph) || strings.Contains(string(r.Body), ph) {
			return fmt.Errorf("kritrim: secret %q may not be sent to %q", ph, host)
		}
		for _, v := range r.Headers {
			if strings.Contains(v, ph) {
				return fmt.Errorf("kritrim: secret %q may not be sent to %q", ph, host)
			}
		}
	}
	return nil
}

func (b *boxHost) secret(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	hosts, ok := b.cfg.Secrets[p.Name]
	if !ok {
		return nil, fmt.Errorf("kritrim: secret %q is not allowed in a sealed agent", p.Name)
	}
	value, err := b.a.env.Secret(ctx, p.Name)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(p.Name + "\x00" + value))
	ph := "kritrim_sec_" + hex.EncodeToString(sum[:8])
	b.smu.Lock()
	b.secrets[ph] = secretBinding{value: value, hosts: hosts}
	b.smu.Unlock()
	return ph, nil
}

func (b *boxHost) tool(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Name   string         `json:"name"`
		Args   map[string]any `json:"args"`
		Thread string         `json:"thread,omitempty"`
		User   string         `json:"user,omitempty"`
		Run    string         `json:"run,omitempty"`
		Roles  []string       `json:"roles,omitempty"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !contains(b.cfg.Bridged, p.Name) {
		return nil, fmt.Errorf("kritrim: tool %q is not bridged into the box", p.Name)
	}
	t, ok := b.a.tool(p.Name)
	if !ok {
		return nil, fmt.Errorf("kritrim: host tool %q not found", p.Name)
	}
	ctx = withIdentity(ctx, p.Thread, p.User, p.Run, p.Roles)
	ctx = context.WithValue(ctx, keyEnv, b.a.env)
	out, err := runTool(ctx, t, p.Args)
	if err != nil {
		return nil, err
	}
	out = b.a.redactor.redact(out)
	b.a.audit(ctx, runOptions{thread: p.Thread, user: p.User}, "tool", t.Name,
		"args="+b.a.redactor.redact(jsonString(p.Args))+" result="+truncate(out, 2000))
	return out, nil
}

func (b *boxHost) approve(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Req    Approval `json:"req"`
		Thread string   `json:"thread,omitempty"`
		User   string   `json:"user,omitempty"`
		Run    string   `json:"run,omitempty"`
		Roles  []string `json:"roles,omitempty"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	ctx = withIdentity(ctx, p.Thread, p.User, p.Run, p.Roles)
	if b.a.cfg.Approve == nil {
		return Reject("approval required and Config.Approve is nil"), nil
	}
	dec, err := b.a.cfg.Approve(ctx, p.Req)
	if err != nil {
		return nil, err
	}
	b.a.audit(ctx, runOptions{thread: p.Thread, user: p.User}, "approval", p.Req.Tool,
		fmt.Sprintf("approve=%v reason=%s", dec.Approve, dec.Reason))
	return dec, nil
}

func withIdentity(ctx context.Context, thread, user, run string, roles []string) context.Context {
	ctx = context.WithValue(ctx, keyThread, thread)
	ctx = context.WithValue(ctx, keyUser, user)
	ctx = context.WithValue(ctx, keyRun, run)
	ctx = context.WithValue(ctx, keyRoles, roles)
	return ctx
}

func (b *boxHost) audit(ctx context.Context, raw json.RawMessage) (any, error) {
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	if b.a.cfg.Audit != nil {
		e.Detail = b.a.redactor.redact(e.Detail)
		if err := b.a.cfg.Audit.Write(ctx, e); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (b *boxHost) run(ctx context.Context, prompt string, emit func(Event), o runOptions) (*Result, error) {
	b.runMu.Lock()
	defer b.runMu.Unlock()
	b.emu.Lock()
	b.emit = emit
	b.emu.Unlock()
	defer func() {
		b.emu.Lock()
		b.emit = nil
		b.emu.Unlock()
	}()

	var msgs []Message
	var pending []Interrupt
	var paused *PausedStep
	if o.thread != "" {
		sess, err := b.a.session.Load(ctx, o.thread)
		if err != nil {
			return nil, err
		}
		if sess != nil {
			msgs, pending, paused = sess.Messages, sess.Pending, sess.Paused
		}
	}
	schema, wrapped, err := outputSchema(o.output)
	if err != nil {
		return nil, err
	}
	params := runParams{Prompt: prompt, Thread: o.thread, Messages: msgs, Pending: pending, Paused: paused,
		Decisions: o.decisions, User: o.user, Roles: o.roles, Images: o.images, Schema: schema, SchemaName: "output"}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			b.p.notify("run.cancel", nil)
		case <-done:
		}
	}()
	var res Result
	err = b.p.call(ctx, "run.start", params, &res)
	close(done)
	if err != nil {
		return nil, err
	}
	if o.thread != "" {
		if err := b.a.session.Save(ctx, &Session{ID: o.thread, Messages: res.Messages,
			Pending: res.Interrupts, Paused: res.Paused, Updated: now()}); err != nil {
			return nil, err
		}
	}
	if o.output != nil {
		if err := decodeOutput(o.output, res.Text, wrapped); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrOutput, err)
		}
	}
	return &res, nil
}

func (b *boxHost) close() error {
	if b.proc != nil {
		_ = b.proc.Kill()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return b.m.Close(ctx)
}

func hostListed(host string, rules []string) bool {
	for _, r := range rules {
		r = strings.ToLower(strings.TrimSpace(r))
		if r == host {
			return true
		}
		if strings.HasPrefix(r, "*.") && strings.HasSuffix(host, r[1:]) {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
