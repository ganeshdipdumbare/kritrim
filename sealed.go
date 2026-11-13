package kritrim

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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
	Run        string              `json:"run,omitempty"`
}

// activeRun is the host's own record of the run in progress. Identity and
// approvals come from here, never from what the box claims.
type activeRun struct {
	thread, user, run string
	roles             []string
	grants            map[string]grant // call id -> approved risky call
	used              int              // tokens served to this run

	ctx    context.Context // canceled when the run ends
	cancel context.CancelFunc
	leases sync.WaitGroup // capability calls in flight
}

func newActiveRun(parent context.Context, thread, user, run string, roles []string) *activeRun {
	r := &activeRun{thread: thread, user: user, run: run, roles: roles, grants: map[string]grant{}}
	r.ctx, r.cancel = context.WithCancel(parent)
	return r
}

// grant is one approved call of a risky tool, bound to its exact arguments.
type grant struct {
	tool string
	args string
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

	umu  sync.Mutex // serializes model calls so the budget check is exact
	used int

	smu      sync.Mutex
	secrets  map[string]secretBinding
	secretPH map[string]string // secret name -> placeholder

	cmu sync.Mutex
	cur *activeRun

	cleanup func()

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
	if cfg.Limits.Tokens == 0 {
		cfg.Limits.Tokens = DefaultTokenLimit
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
	// A fresh, unguessable path: on Local it is a private temp dir, so
	// nothing another user planted can be followed or swapped.
	boxBin := "/tmp/kritrim-agent-" + newID()
	cleanup := func() {}
	if _, local := s.Sandbox.(localSandbox); local {
		dir, err := os.MkdirTemp("", "kritrim-box-")
		if err != nil {
			_ = m.Close(ctx)
			return nil, err
		}
		boxBin = filepath.Join(dir, "agent")
		cleanup = func() { _ = os.RemoveAll(dir) }
	}
	if err := m.Write(ctx, boxBin, data, 0o755); err != nil {
		cleanup()
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
		secrets: map[string]secretBinding{}, secretPH: map[string]string{}, cleanup: cleanup}
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
	var defs []bridgedTool
	for _, name := range b.cfg.Bridged {
		if t, ok := b.a.tool(name); ok {
			defs = append(defs, bridgedTool{Tool: t, Risky: t.risky()})
		}
	}
	if len(defs) == 0 {
		return nil
	}
	return b.p.call(ctx, "tools.add", defs, nil)
}

// bridgedTool is a host tool definition sent to the box. Risky lets the box
// run its normal approval flow; the host still enforces it.
type bridgedTool struct {
	Tool
	Risky bool `json:"risky,omitempty"`
}

// acquire leases the run in progress for one capability call. The returned
// context ends when either the RPC or the run ends, and the run does not
// finish until every lease is released, so no host capability outlives it.
func (b *boxHost) acquire(ctx context.Context) (context.Context, *activeRun, func(), error) {
	b.cmu.Lock()
	r := b.cur
	if r == nil {
		b.cmu.Unlock()
		return nil, nil, nil, errors.New("kritrim: no run in progress")
	}
	r.leases.Add(1)
	b.cmu.Unlock()
	cctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.ctx, cancel)
	cctx = withIdentity(cctx, r.thread, r.user, r.run, r.roles)
	return cctx, r, func() { stop(); cancel(); r.leases.Done() }, nil
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
	ctx, r, release, err := b.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	p.Request.Model = b.a.Model()
	// One model call at a time, and both budgets (the sealed agent's total
	// and this run's Limits.Tokens) are checked before the call against the
	// estimated input, so a box cannot spend past them.
	b.umu.Lock()
	defer b.umu.Unlock()
	if err := fitBudget(&p.Request, b.used, b.cfg.MaxTokens); err != nil {
		return nil, fmt.Errorf("kritrim: sealed %w", err)
	}
	if err := fitBudget(&p.Request, r.used, b.a.cfg.Limits.Tokens); err != nil {
		return nil, err
	}
	resp, err := b.a.model.Stream(ctx, p.Request, func(d Delta) {
		b.p.notify("model.delta", map[string]any{"call": p.Call, "text": d.Text, "reasoning": d.Reasoning})
	})
	if err != nil {
		return nil, err
	}
	b.used += resp.Usage.Total()
	r.used += resp.Usage.Total()
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
	ctx, _, release, err := b.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	r.AllowHosts = allow
	r.sensitive = false
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
		if strings.Contains(r.URL, ph) || bytes.Contains(r.Body, []byte(ph)) {
			r.sensitive = true
		}
		for _, v := range r.Headers {
			if strings.Contains(v, ph) {
				r.sensitive = true
			}
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
	b.smu.Lock()
	defer b.smu.Unlock()
	ph, ok := b.secretPH[p.Name]
	if !ok {
		// Random, so the placeholder reveals nothing about the value.
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return nil, err
		}
		ph = "kritrim_sec_" + hex.EncodeToString(buf[:])
		b.secretPH[p.Name] = ph
	}
	b.secrets[ph] = secretBinding{value: value, hosts: hosts}
	return ph, nil
}

func (b *boxHost) tool(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
		Call string         `json:"call,omitempty"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	ctx, r, release, err := b.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if !contains(b.cfg.Bridged, p.Name) {
		return nil, fmt.Errorf("kritrim: tool %q is not bridged into the box", p.Name)
	}
	t, ok := b.a.tool(p.Name)
	if !ok {
		return nil, fmt.Errorf("kritrim: host tool %q not found", p.Name)
	}
	o := runOptions{thread: r.thread, user: r.user}
	if t.risky() {
		// The host enforces approval itself: a risky bridged tool runs only
		// for a call the host approved, with exactly the approved arguments.
		b.cmu.Lock()
		g, ok := r.grants[p.Call]
		if ok {
			delete(r.grants, p.Call)
		}
		b.cmu.Unlock()
		if !ok || p.Call == "" || g.tool != p.Name || g.args != canonicalArgs(p.Args) {
			b.a.audit(ctx, o, "denied", t.Name, "risky bridged tool called without host approval")
			return nil, fmt.Errorf("kritrim: tool %q needs approval", p.Name)
		}
	}
	if err := t.checkArgs(p.Args); err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, keyEnv, b.a.env)
	out, err := runTool(ctx, t, p.Args)
	if err != nil {
		return nil, err
	}
	out = b.a.redactor.redact(out)
	b.a.audit(ctx, o, "tool", t.Name,
		"args="+b.a.redactor.redact(jsonString(p.Args))+" result="+truncate(out, 2000))
	return out, nil
}

func canonicalArgs(args map[string]any) string {
	if args == nil {
		args = map[string]any{}
	}
	b, _ := json.Marshal(args) // map keys are sorted
	return string(b)
}

func (b *boxHost) approve(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Req Approval `json:"req"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	ctx, r, release, err := b.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	o := runOptions{thread: r.thread, user: r.user}
	if _, ok := b.a.tool(p.Req.Tool); !ok {
		return Reject("unknown tool"), nil
	}
	// The approver sees a message built by the host, not text from the box.
	p.Req.Message = fmt.Sprintf("Approve %s with %s?", p.Req.Tool, jsonString(p.Req.Args))
	if b.a.cfg.Approve == nil {
		return Reject("approval required and Config.Approve is nil"), nil
	}
	dec, err := b.a.cfg.Approve(ctx, p.Req)
	if err != nil {
		return nil, err
	}
	b.grant(r, p.Req.CallID, p.Req.Tool, p.Req.Args, dec)
	b.a.audit(ctx, o, "approval", p.Req.Tool,
		fmt.Sprintf("approve=%v reason=%s", dec.Approve, dec.Reason))
	return dec, nil
}

// grant records an approved call so the bridged tool may run once.
func (b *boxHost) grant(r *activeRun, callID, tool string, args map[string]any, dec Decision) {
	if callID == "" || dec.Pause || dec.Respond != "" || (!dec.Approve && dec.Args == nil) {
		return
	}
	if dec.Args != nil {
		args = dec.Args
	}
	b.cmu.Lock()
	r.grants[callID] = grant{tool: tool, args: canonicalArgs(args)}
	b.cmu.Unlock()
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
	ctx, r, release, err := b.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if b.a.cfg.Audit != nil {
		// Entries from the box are marked and carry the host's identity;
		// seq, time and hashes are assigned by the sink.
		e.Source = "box"
		e.Run, e.Thread, e.User = r.run, r.thread, r.user
		e.Seq, e.Time, e.Prev, e.Hash = 0, 0, "", ""
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
	b.a.identity(&o)
	if o.onEvent != nil {
		emit = o.onEvent
	}
	if o.runID == "" {
		o.runID = newID()
	}
	b.emu.Lock()
	b.emit = emit
	b.emu.Unlock()
	cur := newActiveRun(context.WithoutCancel(ctx), o.thread, o.user, o.runID, o.roles)
	b.cmu.Lock()
	b.cur = cur
	b.cmu.Unlock()
	defer func() {
		// End the run: refuse new leases, cancel in-flight calls and wait
		// for them, so nothing the box queued runs afterwards.
		b.cmu.Lock()
		b.cur = nil
		b.cmu.Unlock()
		cur.cancel()
		cur.leases.Wait()
		b.emu.Lock()
		b.emit = nil
		b.emu.Unlock()
	}()

	var msgs []Message
	var pending []Interrupt
	var paused *PausedStep
	sess := &Session{ID: o.thread}
	if o.thread != "" {
		loaded, err := b.a.session.Load(ctx, o.thread)
		if err != nil {
			return nil, err
		}
		if loaded != nil {
			sess = loaded
			msgs, pending, paused = loaded.Messages, loaded.Pending, loaded.Paused
		}
	}
	// Decisions supplied to resume a paused thread are host approvals too;
	// bind each to the interrupt the host stored.
	for _, in := range pending {
		if d, ok := o.decisions[in.ID]; ok {
			b.grant(cur, in.ID, in.Tool, in.Args, d)
		}
	}
	schema, wrapped, err := outputSchema(o.output)
	if err != nil {
		return nil, err
	}
	params := runParams{Prompt: prompt, Thread: o.thread, Messages: msgs, Pending: pending, Paused: paused,
		Decisions: o.decisions, User: o.user, Roles: o.roles, Images: o.images, Schema: schema, SchemaName: "output",
		Run: o.runID}
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
		sess.Messages, sess.Pending, sess.Paused = res.Messages, res.Interrupts, res.Paused
		if err := b.a.session.Save(ctx, sess); err != nil {
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
	err := b.m.Close(ctx)
	if b.cleanup != nil {
		b.cleanup()
	}
	return err
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
