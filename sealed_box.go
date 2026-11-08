package kritrim

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
)

// InBox reports whether this process is a sealed box. A program that uses
// Sealed checks it first and, when true, calls ServeBox with the same
// configuration instead of creating a host agent.
func InBox() bool { return os.Getenv("KRITRIM_BOX") == "1" }

// ServeBox runs the agent inside a sealed box, driven by the host over r/w.
// It returns when the connection closes or ctx is done.
func ServeBox(ctx context.Context, r io.Reader, w io.Writer, cfg Config) error {
	p := newPeer(r, w)
	a := newBoxAgent(cfg, p)
	p.handle("ping", func(context.Context, json.RawMessage) (any, error) { return map[string]bool{"ok": true}, nil })
	p.handle("model.delta", func(_ context.Context, raw json.RawMessage) (any, error) {
		var d struct {
			Call      string `json:"call"`
			Text      string `json:"text"`
			Reasoning string `json:"reasoning"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		a.model.(*hostModel).delta(d.Call, Delta{Text: d.Text, Reasoning: d.Reasoning})
		return nil, nil
	})
	var (
		mu     sync.Mutex
		cancel context.CancelFunc
		runMu  sync.Mutex
	)
	p.handle("run.cancel", func(context.Context, json.RawMessage) (any, error) {
		mu.Lock()
		if cancel != nil {
			cancel()
		}
		mu.Unlock()
		return nil, nil
	})
	p.handle("run.start", func(_ context.Context, raw json.RawMessage) (any, error) {
		var req runParams
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		runMu.Lock()
		defer runMu.Unlock()
		rctx, rcancel := context.WithCancel(context.Background())
		mu.Lock()
		cancel = rcancel
		mu.Unlock()
		defer rcancel()
		res, err := a.boxRun(rctx, req, p)
		if err != nil {
			return nil, err
		}
		return res, nil
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.Done():
		return nil
	}
}

func newBoxAgent(cfg Config, p *peer) *Agent {
	bridged := []string{}
	if cfg.Sealed != nil {
		bridged = cfg.Sealed.Bridged
	}
	cfg.Sealed = nil
	cfg.Sandbox = nil
	cfg.Tools = append([]Tool(nil), cfg.Tools...)
	if cfg.Limits.Steps == 0 {
		cfg.Limits.Steps = 20
	}
	if cfg.Limits.ToolCalls == 0 {
		cfg.Limits.ToolCalls = 100
	}
	hm := &hostModel{p: p, chans: map[string]chan Delta{}}
	cfg.Model = hm
	cfg.Session = MemorySessions()
	cfg.Approve = func(ctx context.Context, req Approval) (Decision, error) {
		var d Decision
		params := map[string]any{"req": req, "thread": ThreadFrom(ctx), "user": UserFrom(ctx),
			"run": RunIDFrom(ctx), "roles": RolesFrom(ctx)}
		if err := p.call(ctx, "approve", params, &d); err != nil {
			return d, err
		}
		return d, nil
	}
	cfg.Audit = rpcAudit{p: p}
	for i, t := range cfg.Tools {
		if contains(bridged, t.Name) {
			cfg.Tools[i] = proxyTool(p, t)
		}
	}
	a := &Agent{cfg: cfg, model: hm, session: cfg.Session, redactor: &redactor{}}
	a.env = &boxEnv{p: p}
	return a
}

func (a *Agent) boxRun(ctx context.Context, req runParams, p *peer) (*Result, error) {
	mem := a.session.(*memSessions)
	_ = mem.Save(ctx, &Session{ID: req.Thread, Messages: req.Messages})
	opts := []Option{Thread(req.Thread), User(req.User), Roles(req.Roles...), Images(req.Images...)}
	opts = append(opts, withSchema(req.SchemaName, req.Schema))
	res, err := a.run(ctx, req.Prompt, func(e Event) {
		if e.Err != nil {
			e.Text = e.Err.Error()
			e.Err = nil
		}
		p.notify("event", e)
	}, opts...)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ---- box model ----

type hostModel struct {
	p     *peer
	mu    sync.Mutex
	chans map[string]chan Delta
}

func (m *hostModel) Name() string { return "sealed" }

func (m *hostModel) Generate(ctx context.Context, req Request) (*Response, error) {
	return m.call(ctx, req, "")
}

func (m *hostModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	call := newID()
	ch := make(chan Delta, 256)
	m.mu.Lock()
	m.chans[call] = ch
	m.mu.Unlock()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for d := range ch {
			emit(d)
		}
	}()
	resp, err := m.call(ctx, req, call)
	m.mu.Lock()
	delete(m.chans, call)
	close(ch)
	m.mu.Unlock()
	wg.Wait()
	return resp, err
}

func (m *hostModel) call(ctx context.Context, req Request, call string) (*Response, error) {
	var resp Response
	err := m.p.call(ctx, "model", map[string]any{"call": call, "request": req}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (m *hostModel) delta(call string, d Delta) {
	if call == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := m.chans[call]
	if ch == nil {
		return
	}
	select {
	case ch <- d:
	default: // a slow consumer drops deltas; the final response is authoritative
	}
}

// ---- box env ----

type boxEnv struct{ p *peer }

func (e *boxEnv) HTTP(ctx context.Context, r httpRequest) (*httpResponse, error) {
	var out httpResponse
	if err := e.p.call(ctx, "http", r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (e *boxEnv) Exec(ctx context.Context, r execRequest) (execResult, error) {
	return localExec(ctx, r, false)
}

func (e *boxEnv) ReadFile(ctx context.Context, r fsRequest) ([]byte, error) {
	return localRead(r)
}

func (e *boxEnv) WriteFile(ctx context.Context, r fsRequest) error { return localWrite(r) }

func (e *boxEnv) Secret(ctx context.Context, name string) (string, error) {
	var out string
	if err := e.p.call(ctx, "secret", map[string]string{"name": name}, &out); err != nil {
		return "", err
	}
	return out, nil
}

// ---- box tools ----

func proxyTool(p *peer, t Tool) Tool {
	orig := t
	t.run = func(ctx context.Context, args map[string]any) (string, error) {
		var out string
		params := map[string]any{"name": orig.Name, "args": args, "thread": ThreadFrom(ctx),
			"user": UserFrom(ctx), "run": RunIDFrom(ctx), "roles": RolesFrom(ctx)}
		if err := p.call(ctx, "tool", params, &out); err != nil {
			return "", err
		}
		return out, nil
	}
	return t
}

type rpcAudit struct{ p *peer }

func (a rpcAudit) Write(ctx context.Context, e Entry) error {
	return a.p.call(ctx, "audit", e, nil)
}
