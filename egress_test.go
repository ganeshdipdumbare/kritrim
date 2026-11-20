package kritrim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func testHostEnv(cfg Config) *hostEnv {
	return &hostEnv{a: &Agent{cfg: cfg, redactor: &redactor{}}}
}

func srvPort(t *testing.T, raw string) int {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(u.Port())
	return p
}

func TestPathGlobs(t *testing.T) {
	cases := []struct {
		path string
		pats []string
		want bool
	}{
		{"/repos/a/issues", []string{"/repos/*/issues"}, true},
		{"/repos/a/b/issues", []string{"/repos/*/issues"}, false},
		{"/repos/a/issues/1/comments", []string{"/repos/*/issues/**"}, true},
		{"/repos/a/issues", []string{"/repos/*/issues/**"}, true},
		{"/repos/a/pulls", []string{"/repos/*/issues/**"}, false},
		{"/repos/a/../admin", []string{"/repos/**"}, false},
		{"/repos/a/%2e%2e/admin", []string{"/repos/**"}, false},
		{"/repos/a%2Fb/issues", []string{"/repos/*/issues"}, true}, // %2F stays inside one segment
		{"/", []string{"/"}, true},
		{"/x", []string{"/"}, false},
		{"/repos//issues", []string{"/repos/*/issues"}, false},
	}
	for _, c := range cases {
		if got := pathAllowed(c.path, c.pats); got != c.want {
			t.Errorf("pathAllowed(%q, %q) = %v, want %v", c.path, c.pats, got, c.want)
		}
	}
}

func TestEndpointValidate(t *testing.T) {
	bad := []Endpoint{{Host: ""}, {Host: "*"}, {Host: "a.*.com"}, {Host: "h:80"},
		{Host: "h", Ports: []int{0}}, {Host: "h", Methods: []string{"TRACE"}}, {Host: "h", Paths: []string{"x"}}}
	for _, e := range bad {
		if e.validate() == nil {
			t.Errorf("%+v validated", e)
		}
	}
	if err := (Endpoint{Host: "*.example.com", Ports: []int{8443}, Methods: []string{"get"}, Paths: []string{"/v1/**"}}).validate(); err != nil {
		t.Fatal(err)
	}
	if HTTP(HTTPConfig{Endpoints: []Endpoint{{Host: "*"}}}).err == nil {
		t.Fatal("HTTP accepted an invalid endpoint")
	}
}

func TestEndpointEnforcesMethodPathPort(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	port := srvPort(t, srv.URL)
	e := testHostEnv(Config{})
	eps := []Endpoint{{Host: "127.0.0.1", Ports: []int{port}, Methods: []string{"GET"}, Paths: []string{"/ok/**"}}}
	ctx := context.Background()

	if _, err := e.HTTP(ctx, httpRequest{Method: "GET", URL: srv.URL + "/ok/a", Endpoints: eps}); err != nil {
		t.Fatalf("allowed request refused: %v", err)
	}
	for _, r := range []httpRequest{
		{Method: "POST", URL: srv.URL + "/ok/a"},
		{Method: "GET", URL: srv.URL + "/no"},
		{Method: "GET", URL: srv.URL + "/ok/../no"},
		{Method: "GET", URL: "http://127.0.0.1:1/ok/a"},
	} {
		r.Endpoints = eps
		if _, err := e.HTTP(ctx, r); err == nil {
			t.Errorf("%s %s was allowed", r.Method, r.URL)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("server saw %d requests, want 1", hits.Load())
	}
}

func TestEndpointDefaultPortOnly(t *testing.T) {
	u, _ := url.Parse("https://api.example.com:6379/x")
	if m := matchEndpoints("GET", u, []Endpoint{{Host: "api.example.com"}}); m.ok {
		t.Fatal("an endpoint without Ports allowed a non-default port")
	}
	u, _ = url.Parse("https://api.example.com/x")
	if m := matchEndpoints("GET", u, []Endpoint{{Host: "api.example.com"}}); !m.ok || !m.exact {
		t.Fatalf("default port refused: %+v", m)
	}
}

func TestEndpointAuditModeLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	var logged []string
	e := testHostEnv(Config{Logger: func(s string) { logged = append(logged, s) }})
	eps := []Endpoint{{Host: "127.0.0.1", Ports: []int{srvPort(t, srv.URL)}, Methods: []string{"GET"}, Audit: true}}
	if _, err := e.HTTP(context.Background(), httpRequest{Method: "DELETE", URL: srv.URL + "/x", Endpoints: eps}); err != nil {
		t.Fatalf("audit-mode request refused: %v", err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "egress audit") {
		t.Fatalf("audit violation not logged: %q", logged)
	}
}

// A redirect must satisfy the endpoint rules too, not only the first hop.
func TestEndpointRedirectChecked(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
		hits.Add(1)
	}))
	defer srv.Close()
	e := testHostEnv(Config{})
	eps := []Endpoint{{Host: "127.0.0.1", Ports: []int{srvPort(t, srv.URL)}, Paths: []string{"/ok"}}}
	if _, err := e.HTTP(context.Background(), httpRequest{URL: srv.URL + "/ok", Endpoints: eps}); err == nil {
		t.Fatal("redirect to a path outside the rule was followed")
	}
	if hits.Load() != 0 {
		t.Fatal("redirect target was reached")
	}
}

func TestSecretBinding(t *testing.T) {
	e := testHostEnv(Config{SecretHosts: map[string][]string{"GH": {"api.github.com"}}})
	if err := e.secretAllowed("GH", "api.github.com", false); err != nil {
		t.Fatal(err)
	}
	if e.secretAllowed("GH", "evil.github.com", true) == nil {
		t.Fatal("bound secret sent to an unbound host")
	}
	// Unbound secrets go to exact hosts only.
	if e.secretAllowed("OTHER", "x.example.com", false) == nil {
		t.Fatal("unbound secret sent to a host matched by a wildcard")
	}
	if err := e.secretAllowed("OTHER", "x.example.com", true); err != nil {
		t.Fatal(err)
	}
}

// End to end: a secret bound elsewhere never reaches an allowed host.
func TestSecretNotSentToUnboundHost(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Authorization"))
	}))
	defer srv.Close()
	e := testHostEnv(Config{Secrets: map[string]string{"TOK": "s3cret"},
		SecretHosts: map[string][]string{"TOK": {"api.example.com"}}})
	_, err := e.HTTP(context.Background(), httpRequest{URL: srv.URL, AllowHosts: []string{"127.0.0.1"},
		Headers: map[string]string{"Authorization": "secret://TOK"}})
	if err == nil {
		t.Fatal("secret was sent to a host it is not bound to")
	}
	if got.Load() != nil {
		t.Fatal("request reached the server")
	}
	// A missing secret is an error, not the literal reference on the wire.
	_, err = e.HTTP(context.Background(), httpRequest{URL: srv.URL, AllowHosts: []string{"127.0.0.1"},
		Headers: map[string]string{"Authorization": "secret://KRITRIM_TEST_MISSING_SECRET"}})
	if err == nil {
		t.Fatal("missing secret did not fail the request")
	}
}

// The box may not widen the host's egress policy with its own endpoints.
func TestSealedBoxEndpointsIgnored(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Store(true) }))
	defer srv.Close()
	b := testBoxHost(t, Config{}, Sealed{})
	b.cur = &activeRun{ctx: context.Background()}
	raw, _ := json.Marshal(httpRequest{URL: srv.URL, Method: "GET",
		Endpoints: []Endpoint{{Host: "127.0.0.1", Ports: []int{srvPort(t, srv.URL)}}}})
	if _, err := b.http(context.Background(), raw); err == nil {
		t.Fatal("box-supplied endpoint was honoured")
	}
	if hit.Load() {
		t.Fatal("request reached the network")
	}
}
