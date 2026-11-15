package kritrim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// The Local sandbox runs on the host. It must not count as a sandbox, or
// the python tool skips its AllowUnsafeHost guard.
func TestLocalSandboxIsNotASandbox(t *testing.T) {
	a := &Agent{cfg: Config{Sandbox: Local()}, redactor: &redactor{}}
	m, err := Local().Open(context.Background(), OpenConfig{})
	if err != nil {
		t.Fatal(err)
	}
	a.machine = m
	e := &hostEnv{a: a}
	if e.sandboxed() {
		t.Fatal("Local() reports sandboxed, so python runs on the host without AllowUnsafeHost")
	}
}

// A sealed box must not reach host secrets through secret:// references in
// its own request. Only the placeholder RPC may hand out secrets.
func TestSealedBoxCannotResolveHostSecretsInHTTP(t *testing.T) {
	const name = "KRITRIM_TEST_HOST_SECRET"
	t.Setenv(name, "host-secret-value")
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
	}))
	defer srv.Close()

	b := testBoxHost(t, Config{}, Sealed{Egress: []string{"127.0.0.1"}})
	b.cur = &activeRun{ctx: context.Background()}
	raw, err := json.Marshal(httpRequest{URL: srv.URL, Method: "GET",
		Headers: map[string]string{"Authorization": "secret://" + name}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.http(context.Background(), raw)
	if err == nil {
		t.Fatal("secret:// reference in a box request was accepted")
	}
	if hit.Load() {
		t.Fatal("request with a host secret reached the network")
	}
	_ = os.Unsetenv(name)
}

// A signed record copied under another thread's key must not load there.
// The record's own signature still verifies, so only the key check catches it.
func TestSignedSessionsRejectRecordGraftedAcrossThreads(t *testing.T) {
	key := []byte("a-32-byte-key-for-the-test-only!")
	ctx := context.Background()
	inner := MemorySessions()
	store := SignedSessions(&graftStore{SessionStore: inner, from: "attacker", to: "victim"}, key)

	if err := store.Save(ctx, &Session{ID: "attacker", Messages: []Message{{Role: RoleAssistant, Content: "poisoned"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "victim"); !errors.Is(err, ErrSessionTampered) {
		t.Fatalf("a signed record grafted onto another thread loaded: %v", err)
	}
}

// graftStore serves the record stored under `from` when asked for `to`,
// which is what a writer who can rewrite the store's keys would produce.
type graftStore struct {
	SessionStore
	from, to string
}

func (g *graftStore) Load(ctx context.Context, id string) (*Session, error) {
	if id == g.to {
		id = g.from
	}
	return g.SessionStore.Load(ctx, id)
}

// A decision authorizes the one call it was given for. A later call that
// reuses the same call ID with other arguments must ask again.
func TestResumeDecisionDoesNotReplay(t *testing.T) {
	dir := t.TempDir()
	var ran atomic.Int32
	refund := Risky(NewTool("refund", "refund", func(ctx context.Context, in struct {
		Order string `json:"order"`
	}) (string, error) {
		ran.Add(1)
		return "refunded " + in.Order, nil
	}))

	first := &spyModel{turns: []Turn{TurnTool("refund", map[string]any{"order": "A-17"})}}
	a1 := newAgent(t, Config{Model: first, Tools: []Tool{refund}, Session: FileSessions(dir),
		Approve: func(ctx context.Context, req Approval) (Decision, error) { return Pause(), nil }})
	res, err := a1.Run(context.Background(), "refund A-17", Thread("t1"))
	if err != nil || res.Stop != "interrupt" {
		t.Fatalf("stop=%v err=%v", res, err)
	}

	// The resumed run has the same call ID, but a model that now asks for a
	// different order. Nobody approved that call.
	second := &spyModel{turns: []Turn{TurnTool("refund", map[string]any{"order": "EVIL"}), {Text: "done"}}}
	a2 := newAgent(t, Config{Model: second, Tools: []Tool{refund}, Session: FileSessions(dir)})
	_, _ = a2.Run(context.Background(), "", Thread("t1"),
		Decisions(map[string]Decision{res.Interrupts[0].ID: Approve()}))
	if got := ran.Load(); got != 1 {
		t.Fatalf("tool ran %d times; the replayed decision approved a different call", got)
	}
}

// Env values must not appear on the docker command line, where any local
// user can read them from ps. Names go on argv; values go in the client env.
func TestDockerEnvValuesStayOffArgv(t *testing.T) {
	args := dockerExecArgs("box", ExecRequest{Command: []string{"true"}, Env: []string{"TOKEN=s3cret-value"}})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "s3cret-value") {
		t.Fatalf("value on argv: %s", joined)
	}
	if !strings.Contains(joined, "-e TOKEN") {
		t.Fatalf("name not passed: %s", joined)
	}
	found := false
	for _, e := range clientEnv([]string{"TOKEN=s3cret-value"}) {
		if e == "TOKEN=s3cret-value" {
			found = true
		}
	}
	if !found {
		t.Fatal("value not in the docker client environment")
	}
}

// The Local machine runs on the host, so it enforces the allowlist itself
// when one is given.
func TestLocalMachineEnforcesAllowlist(t *testing.T) {
	ctx := context.Background()
	m, err := Local().Open(ctx, OpenConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(ctx)
	if _, err := m.Exec(ctx, ExecRequest{Command: []string{"echo", "x"}, Allow: []string{"true"}}); err == nil {
		t.Fatal("command outside the allowlist ran on the host")
	}
	if _, err := m.Exec(ctx, ExecRequest{Command: []string{"echo", "x"}, Allow: []string{}}); err == nil {
		t.Fatal("an empty allowlist must deny everything")
	}
}

// With the Local sandbox, file tools keep the root jail: a path outside the
// root is refused rather than read from the host.
func TestLocalSandboxKeepsFileRoot(t *testing.T) {
	a := &Agent{cfg: Config{}, redactor: &redactor{}}
	m, err := Local().Open(context.Background(), OpenConfig{})
	if err != nil {
		t.Fatal(err)
	}
	a.machine = m
	e := &hostEnv{a: a}
	if _, err := e.ReadFile(context.Background(), fsRequest{Path: "/etc/hosts", Root: t.TempDir()}); err == nil {
		t.Fatal("file tool read outside its root through the Local sandbox")
	}
}

// Two sinks on one audit log take turns: each append sees the file the
// other one wrote, so the chain stays valid.
func TestAuditSinksShareOneChain(t *testing.T) {
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef")
	path := filepath.Join(t.TempDir(), "shared.log")
	s1 := AuditFile(path, AuditKey(key))
	s2 := AuditFile(path, AuditKey(key))
	for i, s := range []AuditSink{s1, s2, s1, s2} {
		if err := s.Write(ctx, Entry{Kind: "x", Detail: string(rune('a' + i))}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := VerifyAudit(path, AuditKey(key)); err != nil {
		t.Fatalf("interleaved sinks broke the chain: %v", err)
	}
}
