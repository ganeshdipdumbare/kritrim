package a2a

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func testServer(t *testing.T, auth Auth) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var contexts atomic.Int32
	srv, err := NewServer(ServerConfig{
		Card: Card{Name: "test", Description: "test agent"},
		Auth: auth,
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
