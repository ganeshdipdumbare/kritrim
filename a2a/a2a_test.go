package a2a

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func testServer(t *testing.T, auth Auth) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var contexts atomic.Int32
	srv, err := NewServer(ServerConfig{
		Card:             Card{Name: "test", Description: "test agent"},
		Auth:             auth,
		PushAllowPrivate: true,
		Agent: func(ctxID string) (*kritrim.Agent, error) {
			contexts.Add(1)
			refund := kritrim.Risky(kritrim.NewTool("refund", "refund", func(ctx context.Context, in struct {
				Order string `json:"order"`
			}) (string, error) {
				return "refunded " + in.Order, nil
			}))
			return kritrim.New(kritrim.Config{
				Model: kritrim.Mock(
					kritrim.TurnTool("refund", map[string]any{"order": "A-17"}),
					kritrim.Turn{Text: "done"},
				),
				Tools: []kritrim.Tool{refund},
				Approve: func(ctx context.Context, req kritrim.Approval) (kritrim.Decision, error) {
					return kritrim.Pause(), nil
				},
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close(); srv.Close() })
	return ts, &contexts
}

func TestCard(t *testing.T) {
	ts, _ := testServer(t, AllowAll())
	c, err := NewClient(ClientConfig{Endpoint: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	card, err := c.Card(context.Background())
	if err != nil || card.Name != "test" || !card.Capabilities.Streaming {
		t.Fatalf("%+v %v", card, err)
	}
}

func TestSendAndInterruptResume(t *testing.T) {
	ts, _ := testServer(t, AllowAll())
	c, err := NewClient(ClientConfig{Endpoint: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := c.Run(ctx, "refund A-17")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != "interrupt" || len(res.Interrupts) != 1 {
		t.Fatalf("%+v", res)
	}
	res2, err := c.Run(ctx, "", ContextID(res.Thread),
		Decisions(map[string]kritrim.Decision{res.Interrupts[0].ID: kritrim.Approve()}))
	if err != nil {
		t.Fatal(err)
	}
	if res2.Stop != "stop" || res2.Text != "done" {
		t.Fatalf("%+v", res2)
	}
}

func TestAuth(t *testing.T) {
	ts, _ := testServer(t, Bearer("tok"))
	noAuth, _ := NewClient(ClientConfig{Endpoint: ts.URL})
	if _, err := noAuth.Run(context.Background(), "x"); err == nil {
		t.Fatal("unauthenticated request succeeded")
	}
	withAuth, _ := NewClient(ClientConfig{Endpoint: ts.URL, Headers: map[string]string{"Authorization": "Bearer tok"}})
	if _, err := withAuth.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
}

func TestStreamText(t *testing.T) {
	srv, err := NewServer(ServerConfig{
		Card: Card{Name: "s"},
		Auth: AllowAll(),
		Agent: func(ctxID string) (*kritrim.Agent, error) {
			return kritrim.New(kritrim.Config{Model: kritrim.Mock(kritrim.Turn{Text: "hello stream"})})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	defer srv.Close()
	c, _ := NewClient(ClientConfig{Endpoint: ts.URL})
	var text strings.Builder
	for ev, err := range c.Stream(context.Background(), "hi") {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == "text" {
			text.WriteString(ev.Text)
		}
	}
	if text.String() != "hello stream" {
		t.Fatalf("%q", text.String())
	}
}

func TestContextIsolation(t *testing.T) {
	var contexts atomic.Int32
	srv, err := NewServer(ServerConfig{
		Card: Card{Name: "s"},
		Auth: AllowAll(),
		Agent: func(ctxID string) (*kritrim.Agent, error) {
			contexts.Add(1)
			return kritrim.New(kritrim.Config{Model: kritrim.Mock()})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	defer srv.Close()
	c, _ := NewClient(ClientConfig{Endpoint: ts.URL})
	if _, err := c.Run(context.Background(), "a", ContextID("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(context.Background(), "b", ContextID("two")); err != nil {
		t.Fatal(err)
	}
	if contexts.Load() != 2 {
		t.Fatalf("contexts %d", contexts.Load())
	}
}

func TestOAuthClientCredentials(t *testing.T) {
	var form url.Values
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	}))
	defer tokenSrv.Close()
	ts, _ := testServer(t, Bearer("tok"))
	c, err := NewClient(ClientConfig{Endpoint: ts.URL, TokenURL: tokenSrv.URL,
		ClientID: "id", ClientSecret: "secret", Scopes: []string{"mcp:tools"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if form.Get("grant_type") != "client_credentials" || form.Get("client_id") != "id" || form.Get("scope") != "mcp:tools" {
		t.Fatalf("form %+v", form)
	}
}

func TestPushNotifications(t *testing.T) {
	var mu sync.Mutex
	var tasks []Task
	var token string
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		token = r.Header.Get("X-A2A-Notification-Token")
		var task Task
		if err := json.NewDecoder(r.Body).Decode(&task); err == nil {
			tasks = append(tasks, task)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()
	ts, _ := testServer(t, AllowAll())
	c, err := NewClient(ClientConfig{Endpoint: ts.URL, PushURL: recv.URL, PushToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if token != "tok" {
		t.Fatalf("token %q", token)
	}
	if len(tasks) < 2 {
		t.Fatalf("notifications %d", len(tasks))
	}
	states := map[string]bool{}
	for _, task := range tasks {
		states[task.Status.State] = true
	}
	if !states[StateWorking] || !states[StateInputRequired] {
		t.Fatalf("states %v", states)
	}
}

func TestPushConfigRPC(t *testing.T) {
	ts, _ := testServer(t, AllowAll())
	c, _ := NewClient(ClientConfig{Endpoint: ts.URL})
	ctx := context.Background()
	if err := c.SetPush(ctx, "t1", PushConfig{URL: "https://example.com/hook", Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := c.GetPush(ctx, "t1")
	if err != nil || cfg.URL != "https://example.com/hook" {
		t.Fatalf("%+v %v", cfg, err)
	}
	if err := c.DeletePush(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetPush(ctx, "t1"); err == nil {
		t.Fatal("deleted config still present")
	}
}

func TestUnauthorizedStatus(t *testing.T) {
	ts, _ := testServer(t, Bearer("tok"))
	resp, err := http.Post(ts.URL+"/", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestEmptyBearerRejected(t *testing.T) {
	if _, err := NewServer(ServerConfig{Card: Card{Name: "x"}, Auth: Bearer(""),
		Agent: func(string) (*kritrim.Agent, error) { return nil, nil }}); err == nil {
		t.Fatal("empty bearer token accepted")
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer ")
	if Bearer("").Check(r) == nil {
		t.Fatal("empty token authenticated")
	}
	r.Header.Set("Authorization", "Bearer tok")
	if Bearer("tok").Check(r) != nil {
		t.Fatal("valid token refused")
	}
}

func TestPushHeadersNeverResolveSecrets(t *testing.T) {
	t.Setenv("A2A_TEST_SERVER_SECRET", "server-api-key")
	got := make(chan string, 1)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("X-Leak")
	}))
	defer recv.Close()
	srv, err := NewServer(ServerConfig{Card: Card{Name: "x"}, Auth: AllowAll(), PushAllowPrivate: true,
		Agent: func(string) (*kritrim.Agent, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = srv.cfg.Push.Set(ctx, "t1", PushConfig{URL: recv.URL, Headers: map[string]string{"X-Leak": "secret://A2A_TEST_SERVER_SECRET"}})
	srv.notify(ctx, &Task{ID: "t1"})
	if v := <-got; v == "server-api-key" {
		t.Fatal("push header resolved a server secret")
	}
}

func TestPushPrivateURLRefused(t *testing.T) {
	srv, err := NewServer(ServerConfig{Card: Card{Name: "x"}, Auth: AllowAll(),
		Agent: func(string) (*kritrim.Agent, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	c, _ := NewClient(ClientConfig{Endpoint: ts.URL})
	for _, u := range []string{"http://127.0.0.1/hook", "http://169.254.169.254/latest", "file:///etc/passwd"} {
		if err := c.SetPush(context.Background(), "t1", PushConfig{URL: u}); err == nil {
			t.Fatalf("push URL %s accepted", u)
		}
	}
}
