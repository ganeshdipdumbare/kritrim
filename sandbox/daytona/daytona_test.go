package daytona

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ganeshdipdumbare/kritrim/sandbox/sandboxtest"
)

// fakeDaytona implements the Daytona API and toolbox on the local machine.
type fakeDaytona struct {
	t       *testing.T
	mu      sync.Mutex
	polls   int
	created map[string]any
	deleted bool
}

func (f *fakeDaytona) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer k" || r.Header.Get("X-Daytona-Organization-ID") != "org" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/sandbox":
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		w.Write([]byte(`{"id":"d1","state":"creating"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/sandbox/d1":
		f.mu.Lock()
		f.polls++
		f.mu.Unlock()
		fmt.Fprintf(w, `{"id":"d1","state":"started","toolboxProxyUrl":"http://%s/toolbox"}`, r.Host)
	case r.Method == http.MethodDelete && r.URL.Path == "/api/sandbox/d1":
		if r.URL.Query().Get("force") != "true" {
			f.t.Error("delete without force")
		}
		f.deleted = true
	case r.URL.Path == "/toolbox/d1/process/execute":
		var in struct {
			Command string            `json:"command"`
			Cwd     string            `json:"cwd"`
			Timeout int               `json:"timeout"`
			Env     map[string]string `json:"env"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		cmd := exec.Command("sh", "-c", in.Command)
		cmd.Dir = in.Cwd
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		b, _ := json.Marshal(map[string]any{"exitCode": code, "result": string(out)})
		w.Write(b)
	case r.URL.Path == "/toolbox/d1/files/download":
		b, err := os.ReadFile(r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Write(b)
	case r.URL.Path == "/toolbox/d1/files/upload":
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(file)
		if err := os.WriteFile(r.URL.Query().Get("path"), b, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	default:
		http.NotFound(w, r)
	}
}

func TestDaytonaConformance(t *testing.T) {
	f := &fakeDaytona{t: t}
	srv := httptest.NewServer(f)
	defer srv.Close()
	box, err := New(Config{APIKey: "k", Organization: "org", APIURL: srv.URL + "/api", PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sandboxtest.RunWith(t, box, sandboxtest.Options{Dir: t.TempDir(), NoStart: true})
	if f.created["networkBlockAll"] != true || f.polls == 0 || !f.deleted {
		t.Fatalf("created=%v polls=%d deleted=%v", f.created, f.polls, f.deleted)
	}
	if strings.Contains(fmt.Sprint(f.created), "snapshot") {
		t.Fatalf("empty snapshot sent: %v", f.created)
	}
}

func TestDaytonaNeedsKey(t *testing.T) {
	t.Setenv("DAYTONA_API_KEY", "")
	if _, err := New(Config{}); err == nil {
		t.Fatal("no key accepted")
	}
}

func TestDaytonaLive(t *testing.T) {
	if os.Getenv("DAYTONA_API_KEY") == "" {
		t.Skip("DAYTONA_API_KEY not set")
	}
	box, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	sandboxtest.RunWith(t, box, sandboxtest.Options{NoStart: true})
}
