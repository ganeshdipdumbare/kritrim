package kritrim

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A placeholder minted in one run must not be substituted into a request in
// a later run, even when that run is served for a different identity.
func TestSecretBindingDoesNotOutliveRun(t *testing.T) {
	const dummy = "DUMMY-SECRET-VALUE-0001"
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Query().Get("k"))
		mu.Unlock()
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	const host = "127.0.0.1"
	a := &Agent{cfg: Config{Secrets: map[string]string{"K": dummy}}, redactor: &redactor{}}
	a.env = newHostEnv(a)
	b := &boxHost{a: a, cfg: Sealed{Secrets: map[string][]string{"K": {host}}, Egress: []string{host}},
		secrets: map[string]secretBinding{}, secretPH: map[string]string{}}
	ctx := context.Background()

	// Run 1, for alice: the box asks for the secret and receives a placeholder.
	run1 := newActiveRun(ctx, "t1", "alice", "r1", nil)
	b.cur = run1
	raw, _ := json.Marshal(map[string]string{"name": "K"})
	phAny, err := b.secret(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	ph := phAny.(string)

	// End of run 1 through the same method the run-end defer calls.
	b.endRun(run1)

	// Run 2, for bob: the box reuses the placeholder it kept from run 1.
	b.cur = newActiveRun(ctx, "t2", "bob", "r2", nil)
	req, _ := json.Marshal(map[string]any{"Method": "GET", "URL": srv.URL + "/x?k=" + ph})
	_, _ = b.http(ctx, req)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("server saw %d requests in run 2, want 1", len(seen))
	}
	if strings.Contains(seen[0], dummy) {
		t.Fatalf("run-1 credential reached the server in run 2: %q", seen[0])
	}
	if seen[0] != ph {
		t.Fatalf("server saw %q, want the placeholder sent as a literal", seen[0])
	}
}
