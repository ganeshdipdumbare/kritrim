package a2a

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/multiagent"
)

// seeModel records the last user message it was sent.
type seeModel struct {
	mu   sync.Mutex
	last kritrim.Message
}

func (m *seeModel) Generate(ctx context.Context, req kritrim.Request) (*kritrim.Response, error) {
	m.mu.Lock()
	m.last = req.Messages[len(req.Messages)-1]
	m.mu.Unlock()
	return &kritrim.Response{Text: "seen", Stop: kritrim.StopDone}, nil
}

func (m *seeModel) Stream(ctx context.Context, req kritrim.Request, emit func(kritrim.Delta)) (*kritrim.Response, error) {
	return m.Generate(ctx, req)
}

func fileServer(t *testing.T, m *seeModel, cfg ServerConfig) *Client {
	t.Helper()
	cfg.Card = Card{Name: "files"}
	cfg.Auth = AllowAll()
	cfg.Agent = func(string) (*kritrim.Agent, error) { return kritrim.New(kritrim.Config{Model: m}) }
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close(); srv.Close() })
	c, err := NewClient(ClientConfig{Endpoint: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFilePartsReachTheModel(t *testing.T) {
	m := &seeModel{}
	c := fileServer(t, m, ServerConfig{})
	png := []byte("\x89PNG fake")
	_, err := c.Run(context.Background(), "summarize these",
		Files(File{Name: "notes.txt", MimeType: "text/plain", Bytes: []byte("buy milk")},
			File{Name: "pic.png", MimeType: "image/png", Bytes: png}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.last.Content, "summarize these") || !strings.Contains(m.last.Content, "buy milk") ||
		!strings.Contains(m.last.Content, `<file name="notes.txt" type="text/plain">`) {
		t.Fatalf("content = %q", m.last.Content)
	}
	if len(m.last.Images) != 1 || string(m.last.Images[0].Data) != string(png) || m.last.Images[0].MediaType != "image/png" {
		t.Fatalf("images = %+v", m.last.Images)
	}
}

func TestFileOnlyMessage(t *testing.T) {
	m := &seeModel{}
	c := fileServer(t, m, ServerConfig{})
	if _, err := c.Run(context.Background(), "", Files(File{Name: "a.json", MimeType: "application/json", Bytes: []byte(`{"a":1}`)})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.last.Content, `{"a":1}`) {
		t.Fatalf("content = %q", m.last.Content)
	}
}

func TestFilePartsRefused(t *testing.T) {
	m := &seeModel{}
	c := fileServer(t, m, ServerConfig{MaxFileBytes: 8})
	cases := map[string]File{
		"unsupported":  {Name: "a.zip", MimeType: "application/zip", Bytes: []byte("PK")},
		"too large":    {Name: "a.txt", MimeType: "text/plain", Bytes: []byte("0123456789")},
		"no allowlist": {Name: "a.txt", MimeType: "text/plain", URI: "https://example.com/a.txt"},
		"both":         {Name: "a.txt", MimeType: "text/plain", Bytes: []byte("x"), URI: "https://example.com/a.txt"},
		"not utf8":     {Name: "a.txt", MimeType: "text/plain", Bytes: []byte{0xff, 0xfe}},
	}
	for name, f := range cases {
		if _, err := c.Run(context.Background(), "x", Files(f)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFileURIFetchedFromAllowedHost(t *testing.T) {
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/doc.txt", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("remote text"))
	}))
	defer files.Close()
	u, _ := url.Parse(files.URL)
	m := &seeModel{}
	c := fileServer(t, m, ServerConfig{FileHosts: []string{u.Hostname()}})
	if _, err := c.Run(context.Background(), "read", Files(File{Name: "doc.txt", URI: files.URL + "/doc.txt"})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.last.Content, "remote text") {
		t.Fatalf("content = %q", m.last.Content)
	}
	if _, err := c.Run(context.Background(), "read", Files(File{Name: "r.txt", URI: files.URL + "/redirect"})); err == nil {
		t.Fatal("redirect was followed")
	}
}

func TestFileURIWildcardMustBePublic(t *testing.T) {
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }))
	defer files.Close()
	m := &seeModel{}
	// 127.0.0.1 matches no wildcard, and a wildcard never trusts private IPs.
	c := fileServer(t, m, ServerConfig{FileHosts: []string{"*.0.0.1"}})
	if _, err := c.Run(context.Background(), "read", Files(File{Name: "a.txt", MimeType: "text/plain", URI: files.URL})); err == nil {
		t.Fatal("private address fetched through a wildcard")
	}
}

func TestClientAsGraphNode(t *testing.T) {
	m := &seeModel{}
	c := fileServer(t, m, ServerConfig{})
	g, err := multiagent.Sequence(
		multiagent.Step{Name: "prep", Node: func(ctx context.Context, p string, _ *multiagent.State) (*kritrim.Result, error) {
			return &kritrim.Result{Text: "remote: " + p, Stop: kritrim.StopDone}, nil
		}},
		multiagent.Step{Name: "remote", Node: c.Node()},
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Run(context.Background(), "hello")
	if err != nil || res.Text != "seen" || m.last.Content != "remote: hello" {
		t.Fatalf("res=%v err=%v sent=%q", res, err, m.last.Content)
	}
}
