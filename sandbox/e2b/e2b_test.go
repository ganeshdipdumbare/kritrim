package e2b

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/sandbox/sandboxtest"
)

// fakeE2B implements the E2B API and envd on the local machine: commands
// really run, so the conformance suite exercises the wire protocol end to
// end.
type fakeE2B struct {
	t       *testing.T
	mu      sync.Mutex
	stdins  map[uint32]io.WriteCloser
	procs   map[uint32]*exec.Cmd
	created map[string]map[string]any
	deleted []string
}

func newFake(t *testing.T) (*fakeE2B, *httptest.Server) {
	f := &fakeE2B{t: t, stdins: map[uint32]io.WriteCloser{}, procs: map[uint32]*exec.Cmd{}, created: map[string]map[string]any{}}
	return f, httptest.NewServer(f)
}

func (f *fakeE2B) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
		if r.Header.Get("X-API-Key") != "k" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.created["sbx1"] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"sandboxID":"sbx1","envdAccessToken":"tok","domain":"example.test"}`))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/sandboxes/"):
		f.mu.Lock()
		f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/sandboxes/"))
		f.mu.Unlock()
	case r.URL.Path == "/process.Process/Start":
		f.checkEnvd(r)
		f.startProcess(w, r)
	case r.URL.Path == "/process.Process/SendInput":
		f.checkEnvd(r)
		var in struct {
			Process struct{ PID uint32 } `json:"process"`
			Input   struct {
				Stdin []byte `json:"stdin"`
			} `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		stdin := f.stdins[in.Process.PID]
		f.mu.Unlock()
		if stdin != nil {
			stdin.Write(in.Input.Stdin)
		}
		w.Write([]byte("{}"))
	case r.URL.Path == "/process.Process/CloseStdin":
		var in struct {
			Process struct{ PID uint32 } `json:"process"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		if s := f.stdins[in.Process.PID]; s != nil {
			s.Close()
		}
		f.mu.Unlock()
		w.Write([]byte("{}"))
	case r.URL.Path == "/process.Process/SendSignal":
		var in struct {
			Process struct{ PID uint32 } `json:"process"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		if c := f.procs[in.Process.PID]; c != nil && c.Process != nil {
			c.Process.Kill()
		}
		f.mu.Unlock()
		w.Write([]byte("{}"))
	case r.URL.Path == "/files" && r.Method == http.MethodGet:
		f.checkEnvd(r)
		b, err := os.ReadFile(r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Write(b)
	case r.URL.Path == "/files" && r.Method == http.MethodPost:
		f.checkEnvd(r)
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(file)
		if err := os.WriteFile(r.URL.Query().Get("path"), b, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`[]`))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeE2B) checkEnvd(r *http.Request) {
	if r.Header.Get("X-Access-Token") != "tok" || r.Header.Get("E2b-Sandbox-Id") != "sbx1" ||
		!strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
		f.t.Errorf("envd request without sandbox headers: %v", r.Header)
	}
	if r.Header.Get("X-API-Key") != "" {
		f.t.Errorf("API key sent to the sandbox")
	}
}

func (f *fakeE2B) startProcess(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/connect+json" {
		http.Error(w, "want connect+json", http.StatusUnsupportedMediaType)
		return
	}
	_, msg, err := readEnvelope(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		Process struct {
			Cmd  string            `json:"cmd"`
			Args []string          `json:"args"`
			Envs map[string]string `json:"envs"`
			Cwd  string            `json:"cwd"`
		} `json:"process"`
		Stdin bool `json:"stdin"`
	}
	_ = json.Unmarshal(msg, &req)
	cmd := exec.Command(req.Process.Cmd, req.Process.Args...)
	cmd.Dir = req.Process.Cwd
	cmd.Env = os.Environ()
	for k, v := range req.Process.Envs {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdin io.WriteCloser
	if req.Stdin {
		stdin, _ = cmd.StdinPipe()
	}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	w.Header().Set("Content-Type", "application/connect+json")
	fl := w.(http.Flusher)
	var wmu sync.Mutex
	send := func(v any) {
		b, _ := json.Marshal(map[string]any{"event": v})
		wmu.Lock()
		defer wmu.Unlock()
		writeEnvelope(w, 0, b)
		fl.Flush()
	}
	if err := cmd.Start(); err != nil {
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "not_found", "message": err.Error()}})
		writeEnvelope(w, 2, b)
		return
	}
	pid := uint32(cmd.Process.Pid)
	f.mu.Lock()
	f.procs[pid] = cmd
	if stdin != nil {
		f.stdins[pid] = stdin
	}
	f.mu.Unlock()
	send(map[string]any{"start": map[string]any{"pid": pid}})
	var wg sync.WaitGroup
	pump := func(rd io.Reader, key string) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := rd.Read(buf)
			if n > 0 {
				send(map[string]any{"data": map[string]any{key: buf[:n]}})
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(stdout, "stdout")
	go pump(stderr, "stderr")
	wg.Wait()
	code := 0
	if err := cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	send(map[string]any{"end": map[string]any{"exitCode": code, "exited": true}})
	wmu.Lock()
	writeEnvelope(w, 2, []byte("{}"))
	wmu.Unlock()
}

func TestE2BConformance(t *testing.T) {
	f, srv := newFake(t)
	defer srv.Close()
	box, err := New(Config{APIKey: "k", APIURL: srv.URL, EnvdURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	sandboxtest.RunWith(t, box, sandboxtest.Options{Dir: t.TempDir()})
	if f.created["sbx1"]["allow_internet_access"] != false || f.created["sbx1"]["templateID"] != "base" {
		t.Fatalf("create body %v", f.created["sbx1"])
	}
	if len(f.deleted) != 1 || f.deleted[0] != "sbx1" {
		t.Fatalf("deleted %v", f.deleted)
	}
}

// TestE2BSealed runs a sealed agent through the adapter: the box binary is
// uploaded, started with streamed stdio, and keeps running after New.
func TestE2BSealed(t *testing.T) {
	_, srv := newFake(t)
	defer srv.Close()
	bin := filepath.Join(t.TempDir(), "box")
	build := exec.Command("go", "build", "-o", bin, "../../internal/boxtest")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build box: %v: %s", err, out)
	}
	box, _ := New(Config{APIKey: "k", APIURL: srv.URL, EnvdURL: srv.URL})
	refund := kritrim.Risky(kritrim.NewTool("refund", "refund", func(ctx context.Context, in struct {
		Order string `json:"order"`
	}) (string, error) {
		return "refunded " + in.Order, nil
	}))
	a, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(kritrim.TurnTool("refund", map[string]any{"order": "A-17"}), kritrim.Turn{Text: "done"}),
		Tools: []kritrim.Tool{refund},
		Approve: func(ctx context.Context, req kritrim.Approval) (kritrim.Decision, error) {
			return kritrim.Approve(), nil
		},
		Sealed: &kritrim.Sealed{Sandbox: box, Binary: bin},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	res, err := a.Run(context.Background(), "refund A-17")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" || res.Steps[0].ToolResults[0].Content != "refunded A-17" {
		t.Fatalf("%+v", res)
	}
}

func TestE2BNeedsKey(t *testing.T) {
	t.Setenv("E2B_API_KEY", "")
	if _, err := New(Config{}); err == nil {
		t.Fatal("no key accepted")
	}
}

func TestE2BLive(t *testing.T) {
	if os.Getenv("E2B_API_KEY") == "" {
		t.Skip("E2B_API_KEY not set")
	}
	box, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	sandboxtest.Run(t, box)
}

func TestEnvelopeRoundTrip(t *testing.T) {
	var b bytes.Buffer
	writeEnvelope(&b, 2, []byte(`{"x":1}`))
	flags, msg, err := readEnvelope(&b)
	if err != nil || flags != 2 || string(msg) != `{"x":1}` {
		t.Fatalf("%d %s %v", flags, msg, err)
	}
	if _, _, err := readEnvelope(&b); err != io.EOF {
		t.Fatalf("empty stream: %v", err)
	}
}
