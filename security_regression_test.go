package kritrim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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
