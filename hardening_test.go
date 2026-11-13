package kritrim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// ---- files: symlinks cannot leave the root ----

func TestFilesSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "file-link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"link/secret.txt", "file-link"} {
		if b, err := localRead(fsRequest{Path: p, Root: root}); err == nil {
			t.Fatalf("read %s escaped the root: %q", p, b)
		}
	}
	if err := localWrite(fsRequest{Path: "link/new.txt", Root: root, Data: []byte("x")}); err == nil {
		t.Fatal("write through a symlink escaped the root")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Fatal("file created outside the root")
	}
	// Normal paths still work.
	if err := localWrite(fsRequest{Path: "a/b.txt", Root: root, Data: []byte("ok")}); err != nil {
		t.Fatal(err)
	}
	if b, err := localRead(fsRequest{Path: "a/b.txt", Root: root}); err != nil || string(b) != "ok" {
		t.Fatalf("%q %v", b, err)
	}
}

// ---- exec: no host credentials, allowlist always enforced ----

func TestChildEnvDropsSecrets(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-leak")
	env := strings.Join(childEnv([]string{"EXTRA=1"}), "\n")
	if strings.Contains(env, "sk-should-not-leak") {
		t.Fatal("API key inherited by child process")
	}
	if !strings.Contains(env, "EXTRA=1") {
		t.Fatal("extra env missing")
	}
	res, err := localExec(context.Background(), execRequest{Command: []string{"env"}, Allow: []string{"env"}}, true)
	if err != nil {
		t.Skip("env not available:", err)
	}
	if strings.Contains(res.Stdout, "sk-should-not-leak") {
		t.Fatal("API key visible to shell command")
	}
}

func TestExecAllowlistWithSandbox(t *testing.T) {
	a := &Agent{machine: &localMachine{}, redactor: &redactor{}}
	e := &hostEnv{a: a}
	if _, err := e.Exec(context.Background(), execRequest{Command: []string{"rm", "-rf", "x"}, Allow: []string{"echo"}}); err == nil {
		t.Fatal("command outside the allowlist ran in a sandbox")
	}
}

// ---- HTTP: secrets never follow a cross-host redirect ----

func TestSecretNotForwardedOnRedirect(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "" {
			leaked.Store(true)
		}
	}))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(other.URL, "127.0.0.1", "localhost", 1), http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	a := &Agent{cfg: Config{Secrets: map[string]string{"KEY": "super-secret-value"}}, redactor: &redactor{}}
	e := &hostEnv{a: a}
	resp, err := e.HTTP(context.Background(), httpRequest{URL: first.URL, AllowHosts: []string{"127.0.0.1", "localhost"},
		Headers: map[string]string{"X-Api-Key": "secret://KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	if leaked.Load() {
		t.Fatal("secret header followed a redirect to another host")
	}
	if resp.Status != http.StatusTemporaryRedirect {
		t.Fatalf("status %d, want the 3xx returned", resp.Status)
	}
}

func TestHTTPRejectsOddMethodsAndSchemes(t *testing.T) {
	e := &hostEnv{a: &Agent{redactor: &redactor{}}}
	if _, err := e.HTTP(context.Background(), httpRequest{Method: "CONNECT", URL: "http://example.com", AllowHosts: []string{"example.com"}}); err == nil {
		t.Fatal("CONNECT allowed")
	}
	if _, err := e.HTTP(context.Background(), httpRequest{URL: "file://example.com/etc/passwd", AllowHosts: []string{"example.com"}}); err == nil {
		t.Fatal("file scheme allowed")
	}
}

// ---- tool arguments are validated against the schema ----

func TestToolArgsValidated(t *testing.T) {
	var ran atomic.Bool
	tool := NewRawTool("pay", "pay", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"amount": map[string]any{"type": "integer"},
			"mode":   map[string]any{"type": "string", "enum": []any{"card", "bank"}},
		},
		"required":             []any{"amount"},
		"additionalProperties": false,
	}, func(ctx context.Context, args map[string]any) (string, error) { ran.Store(true); return "ok", nil })
	a, err := New(Config{Model: Mock(), Tools: []Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, args := range []map[string]any{
		{},
		{"amount": "100"},
		{"amount": 1.5},
		{"amount": 1, "mode": "cash"},
		{"amount": 1, "extra": true},
	} {
		if _, err := a.CallTool(context.Background(), "pay", args); err == nil {
			t.Fatalf("args %v accepted", args)
		}
	}
	if ran.Load() {
		t.Fatal("tool ran with invalid arguments")
	}
	if out, err := a.CallTool(context.Background(), "pay", map[string]any{"amount": float64(3), "mode": "card"}); err != nil || out != "ok" {
		t.Fatalf("%q %v", out, err)
	}
}

// ---- hooks compose ----

func TestChainHooks(t *testing.T) {
	var order []string
	h := Chain(
		Hooks{BeforeTool: func(ctx context.Context, e *ToolEvent) error { order = append(order, "a"); return nil },
			AfterTool: func(ctx context.Context, e *ToolEvent) { order = append(order, "A") }},
		Hooks{BeforeTool: func(ctx context.Context, e *ToolEvent) error { order = append(order, "b"); e.Deny = "no"; return nil }},
		Hooks{BeforeTool: func(ctx context.Context, e *ToolEvent) error { order = append(order, "c"); return nil },
			AfterTool: func(ctx context.Context, e *ToolEvent) { order = append(order, "C") }},
	)
	e := &ToolEvent{}
	_ = h.BeforeTool(context.Background(), e)
	h.AfterTool(context.Background(), e)
	if got := strings.Join(order, ""); got != "abAC" || e.Deny != "no" {
		t.Fatalf("order %q deny %q", got, e.Deny)
	}
}

// ---- sealed host: the box cannot bypass approval, identity or budget ----

type countModel struct {
	calls atomic.Int32
	usage Usage
}

func (m *countModel) Generate(ctx context.Context, req Request) (*Response, error) {
	m.calls.Add(1)
	return &Response{Text: "ok", Stop: "stop", Usage: m.usage}, nil
}
func (m *countModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	return m.Generate(ctx, req)
}

func testBoxHost(t *testing.T, cfg Config, s Sealed) *boxHost {
	t.Helper()
	a := &Agent{cfg: cfg, model: cfg.Model, redactor: &redactor{}}
	a.env = newHostEnv(a)
	return &boxHost{a: a, cfg: s, secrets: map[string]secretBinding{}, secretPH: map[string]string{}}
}

func TestSealedBridgedRiskyToolNeedsHostApproval(t *testing.T) {
	var ran atomic.Int32
	var seenUser atomic.Value
	refund := Risky(NewTool("refund", "refund", func(ctx context.Context, in struct {
		Order string `json:"order"`
	}) (string, error) {
		ran.Add(1)
		seenUser.Store(UserFrom(ctx))
		return "refunded " + in.Order, nil
	}))
	approve := func(ctx context.Context, req Approval) (Decision, error) {
		if req.Args["order"] == "A-1" {
			return Approve(), nil
		}
		return Reject("no"), nil
	}
	b := testBoxHost(t, Config{Tools: []Tool{refund}, Approve: approve}, Sealed{Bridged: []string{"refund"}})
	call := func(method string, p any) (any, error) {
		raw, _ := json.Marshal(p)
		switch method {
		case "tool":
			return b.tool(context.Background(), raw)
		default:
			return b.approve(context.Background(), raw)
		}
	}
	// No run in progress: nothing is allowed.
	if _, err := call("tool", map[string]any{"name": "refund", "args": map[string]any{"order": "A-1"}, "call": "c1"}); err == nil {
		t.Fatal("tool ran outside a run")
	}
	b.cur = newActiveRun(context.Background(), "", "alice", "r1", nil)
	// A compromised box calls the risky tool directly, claiming to be admin.
	if _, err := call("tool", map[string]any{"name": "refund", "args": map[string]any{"order": "A-1"}, "call": "c1", "user": "admin"}); err == nil {
		t.Fatal("risky bridged tool ran without approval")
	}
	// Approved for A-1, then called with different arguments.
	if _, err := call("approve", map[string]any{"req": Approval{Tool: "refund", CallID: "c2", Args: map[string]any{"order": "A-1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := call("tool", map[string]any{"name": "refund", "args": map[string]any{"order": "Z-9"}, "call": "c2"}); err == nil {
		t.Fatal("approved call ran with swapped arguments")
	}
	// Approved and called as approved: runs once, as the host's user.
	if _, err := call("approve", map[string]any{"req": Approval{Tool: "refund", CallID: "c3", Args: map[string]any{"order": "A-1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := call("tool", map[string]any{"name": "refund", "args": map[string]any{"order": "A-1"}, "call": "c3", "user": "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call("tool", map[string]any{"name": "refund", "args": map[string]any{"order": "A-1"}, "call": "c3"}); err == nil {
		t.Fatal("approval was reusable")
	}
	if ran.Load() != 1 {
		t.Fatalf("ran %d times", ran.Load())
	}
	if u := seenUser.Load(); u != "alice" {
		t.Fatalf("tool saw user %v, want the host's alice", u)
	}
}

func TestSealedBudgetCheckedBeforeCall(t *testing.T) {
	m := &countModel{usage: Usage{In: 60, Out: 60}}
	b := testBoxHost(t, Config{Model: m}, Sealed{MaxTokens: 100})
	b.cur = newActiveRun(context.Background(), "", "", "r1", nil)
	raw, _ := json.Marshal(map[string]any{"request": Request{}})
	if _, err := b.model(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := b.model(context.Background(), raw); err == nil {
			t.Fatal("call allowed past the budget")
		}
	}
	if n := m.calls.Load(); n != 1 {
		t.Fatalf("provider called %d times, want 1", n)
	}
}

func TestSealedAuditEntriesAreMarked(t *testing.T) {
	var got []Entry
	b := testBoxHost(t, Config{Audit: AuditFunc(func(ctx context.Context, e Entry) error { got = append(got, e); return nil })}, Sealed{})
	raw, _ := json.Marshal(Entry{Kind: "approval", User: "admin", Seq: 99, Hash: "forged"})
	if _, err := b.audit(context.Background(), raw); err == nil {
		t.Fatal("audit accepted outside a run")
	}
	b.cur = newActiveRun(context.Background(), "", "alice", "r1", nil)
	if _, err := b.audit(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Source != "box" || got[0].User != "alice" || got[0].Seq != 0 || got[0].Hash != "" {
		t.Fatalf("%+v", got)
	}
}

func TestSealedSecretPlaceholderIsRandom(t *testing.T) {
	b := testBoxHost(t, Config{Secrets: map[string]string{"TOKEN": "abcd1234"}}, Sealed{Secrets: map[string][]string{"TOKEN": {"api.example.com"}}})
	raw, _ := json.Marshal(map[string]string{"name": "TOKEN"})
	ph1, err := b.secret(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	ph2, _ := b.secret(context.Background(), raw)
	b2 := testBoxHost(t, Config{Secrets: map[string]string{"TOKEN": "abcd1234"}}, Sealed{Secrets: map[string][]string{"TOKEN": {"api.example.com"}}})
	ph3, _ := b2.secret(context.Background(), raw)
	if ph1 != ph2 || ph1 == ph3 {
		t.Fatalf("placeholders %v %v %v: want stable per host, different across hosts", ph1, ph2, ph3)
	}
}

// ---- audit: keyed chain detects rewrite and truncation ----

func TestAuditKeyDetectsTampering(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	path := filepath.Join(t.TempDir(), "audit.log")
	sink := AuditFile(path, AuditKey(key))
	for i := 0; i < 3; i++ {
		if err := sink.Write(context.Background(), Entry{Kind: "tool", Name: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := VerifyAudit(path, AuditKey(key)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")

	// Rewrite the whole chain without the key: plain verification passes,
	// keyed verification fails.
	var forged []string
	prev := ""
	for _, l := range lines {
		var e Entry
		_ = json.Unmarshal([]byte(l), &e)
		e.Detail = "forged"
		e.Prev = prev
		e.Hash = hashEntry(e, nil)
		prev = e.Hash
		out, _ := json.Marshal(e)
		forged = append(forged, string(out))
	}
	_ = os.WriteFile(path, []byte(strings.Join(forged, "\n")+"\n"), 0o600)
	if err := VerifyAudit(path); err != nil {
		t.Fatalf("unkeyed verify should pass a recomputed chain: %v", err)
	}
	if err := VerifyAudit(path, AuditKey(key)); err == nil {
		t.Fatal("keyed verify accepted a rewritten log")
	}

	// Truncate the last two entries.
	_ = os.WriteFile(path, []byte(lines[0]+"\n"), 0o600)
	if err := VerifyAudit(path, AuditKey(key)); err == nil {
		t.Fatal("keyed verify accepted a truncated log")
	}
	if err := AuditFile(filepath.Join(t.TempDir(), "x"), AuditKey([]byte("short"))).Write(context.Background(), Entry{}); err == nil {
		t.Fatal("short key accepted")
	}
}

func FuzzPaths(f *testing.F) {
	f.Add("a/b.txt", "/work")
	f.Add("../../etc/passwd", "/work")
	f.Add("/work/../etc", "/work")
	f.Fuzz(func(t *testing.T, p, root string) {
		if root == "" || root[0] != '/' {
			return
		}
		got, err := boxPath(p, root)
		if err != nil {
			return
		}
		clean := filepath.Clean(root)
		if clean != "/" && got != clean && !strings.HasPrefix(got, clean+"/") {
			t.Fatalf("boxPath(%q, %q) = %q escapes", p, root, got)
		}
	})
}

func TestAuditSinkRefusesBadLogs(t *testing.T) {
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef")
	other := []byte("ffffffffffffffffffffffffffffffff")
	dir := t.TempDir()

	// Empty and nil keys are errors, not "no signing".
	for _, k := range [][]byte{nil, {}} {
		if err := AuditFile(filepath.Join(dir, "e.log"), AuditKey(k)).Write(ctx, Entry{Kind: "x"}); err == nil {
			t.Fatalf("key %v accepted", k)
		}
		if err := VerifyAudit(filepath.Join(dir, "e.log"), AuditKey(k)); err == nil {
			t.Fatalf("verify with key %v accepted", k)
		}
	}

	// An unkeyed log is not silently continued under a key.
	plain := filepath.Join(dir, "plain.log")
	_ = AuditFile(plain).Write(ctx, Entry{Kind: "x"})
	if err := AuditFile(plain, AuditKey(key)).Write(ctx, Entry{Kind: "y"}); err == nil {
		t.Fatal("appended keyed entries to an unkeyed log")
	}

	// A keyed log is not continued under another key.
	keyed := filepath.Join(dir, "keyed.log")
	for i := 0; i < 3; i++ {
		_ = AuditFile(keyed, AuditKey(key)).Write(ctx, Entry{Kind: "x"})
	}
	if err := AuditFile(keyed, AuditKey(other)).Write(ctx, Entry{Kind: "y"}); err == nil {
		t.Fatal("appended under a different key")
	}

	// A truncated keyed log is not re-blessed by the next append.
	b, _ := os.ReadFile(keyed)
	first := strings.SplitN(string(b), "\n", 2)[0] + "\n"
	_ = os.WriteFile(keyed, []byte(first), 0o600)
	if err := AuditFile(keyed, AuditKey(key)).Write(ctx, Entry{Kind: "y"}); err == nil {
		t.Fatal("appended to a truncated log")
	}
	if err := VerifyAudit(keyed, AuditKey(key)); err == nil {
		t.Fatal("truncated log verifies")
	}
}

func TestAuditSinkRecoversAfterFailure(t *testing.T) {
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef")
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")
	sink := AuditFile(path, AuditKey(key))
	_ = sink.Write(ctx, Entry{Kind: "x"})
	// Make the head unwritable so the next write fails after appending.
	if err := os.Mkdir(path+".head.tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(ctx, Entry{Kind: "y"}); err == nil {
		t.Fatal("expected head write failure")
	}
	_ = os.Remove(path + ".head.tmp")
	if err := sink.Write(ctx, Entry{Kind: "z"}); err != nil {
		t.Fatalf("sink did not recover: %v", err)
	}
	if err := VerifyAudit(path, AuditKey(key)); err != nil {
		t.Fatalf("log after recovery: %v", err)
	}
}

// ---- review follow-ups ----

func TestBoxExecEnforcesAllowlist(t *testing.T) {
	e := &boxEnv{}
	if _, err := e.Exec(context.Background(), execRequest{Command: []string{"rm", "-rf", "x"}, Allow: []string{"echo"}}); err == nil {
		t.Fatal("box ran a command outside the allowlist")
	}
	if _, err := e.Exec(context.Background(), execRequest{Command: []string{"echo", "x"}}); err == nil {
		t.Fatal("box ran a command with no allowlist")
	}
}

func TestSecretNotSentOverDowngrade(t *testing.T) {
	// Redirect to the same host and port over plain http. If followed, the
	// TLS server answers the plaintext request with 400.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	a := &Agent{cfg: Config{Client: srv.Client(), Secrets: map[string]string{"KEY": "super-secret-value"}}, redactor: &redactor{}}
	resp, err := (&hostEnv{a: a}).HTTP(context.Background(), httpRequest{URL: srv.URL, AllowHosts: []string{"127.0.0.1"},
		Headers: map[string]string{"X-Api-Key": "secret://KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusTemporaryRedirect {
		t.Fatalf("secret followed an https->http redirect on the same host (status %d)", resp.Status)
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWildcardEgressRefusesUnguardableClient(t *testing.T) {
	var called atomic.Bool
	client := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		called.Store(true)
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	})}
	a := &Agent{cfg: Config{Client: client}, redactor: &redactor{}}
	_, err := (&hostEnv{a: a}).HTTP(context.Background(), httpRequest{URL: "https://1.1.1.1/", AllowHosts: []string{"*.example.com", "1.1.1.1"}})
	if err == nil || called.Load() {
		t.Fatal("request sent through a transport the dial guard cannot cover")
	}
	// Exact-only rules need no dial guard, so a custom transport is fine.
	if _, err := (&hostEnv{a: a}).HTTP(context.Background(), httpRequest{URL: "https://1.1.1.1/", AllowHosts: []string{"1.1.1.1"}}); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetCheckedBeforeModelCall(t *testing.T) {
	m := &countModel{usage: Usage{In: 10, Out: 10}}
	a, err := New(Config{Model: m, Limits: Limits{Tokens: 50}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// The prompt alone is far above the budget: the provider is never called.
	if _, err := a.Run(context.Background(), strings.Repeat("word ", 400)); err == nil {
		t.Fatal("run over budget succeeded")
	}
	if n := m.calls.Load(); n != 0 {
		t.Fatalf("provider called %d times for an input that cannot fit", n)
	}
	req := &Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}, MaxTokens: 1000}
	if err := fitBudget(req, 0, 100); err != nil || req.MaxTokens >= 100 {
		t.Fatalf("output not capped to the remaining budget: %d %v", req.MaxTokens, err)
	}
	if err := fitBudget(req, 100, 100); err == nil {
		t.Fatal("exhausted budget allowed a call")
	}
}

func TestSealedHostEnforcesRunTokenLimit(t *testing.T) {
	m := &countModel{usage: Usage{In: 40, Out: 40}}
	b := testBoxHost(t, Config{Model: m, Limits: Limits{Tokens: 100}}, Sealed{}) // no Sealed.MaxTokens
	b.cur = newActiveRun(context.Background(), "", "", "r1", nil)
	raw, _ := json.Marshal(map[string]any{"request": Request{}})
	for i := 0; i < 5; i++ {
		_, _ = b.model(context.Background(), raw)
	}
	if n := m.calls.Load(); n != 2 {
		t.Fatalf("provider called %d times, want 2 within a 100-token run limit", n)
	}
}

func TestSealedCapabilitiesEndWithRun(t *testing.T) {
	started := make(chan struct{})
	var sawCancel atomic.Bool
	slow := NewTool("slow", "slow", func(ctx context.Context, in struct{}) (string, error) {
		close(started)
		<-ctx.Done()
		sawCancel.Store(true)
		return "", ctx.Err()
	})
	b := testBoxHost(t, Config{Tools: []Tool{slow}}, Sealed{Bridged: []string{"slow"}})
	r := newActiveRun(context.Background(), "", "alice", "r1", nil)
	b.cur = r
	raw, _ := json.Marshal(map[string]any{"name": "slow", "args": map[string]any{}, "call": "c1"})
	done := make(chan struct{})
	go func() { _, _ = b.tool(context.Background(), raw); close(done) }()
	<-started
	// End the run the way boxHost.run does.
	b.cmu.Lock()
	b.cur = nil
	b.cmu.Unlock()
	r.cancel()
	r.leases.Wait()
	<-done
	if !sawCancel.Load() {
		t.Fatal("in-flight capability call kept running after the run ended")
	}
	if _, err := b.tool(context.Background(), raw); err == nil {
		t.Fatal("capability call accepted after the run ended")
	}
}
