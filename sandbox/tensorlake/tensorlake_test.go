package tensorlake

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ganeshdipdumbare/kritrim/sandbox/sandboxtest"
)

type line struct{ text, stream string }

type fakeProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
	cond  *sync.Cond
	lines []line
	done  bool
	code  int
}

// fakeTL implements the Tensorlake API and sandbox proxy on the local
// machine.
type fakeTL struct {
	t       *testing.T
	mu      sync.Mutex
	procs   map[int64]*fakeProc
	polls   int
	created map[string]any
	deleted bool
}

func (f *fakeTL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer k" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPost && p == "/sandboxes":
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		w.Write([]byte(`{"sandbox_id":"sb1","status":"pending"}`))
	case r.Method == http.MethodGet && p == "/sandboxes/sb1":
		f.mu.Lock()
		f.polls++
		f.mu.Unlock()
		fmt.Fprintf(w, `{"sandbox_id":"sb1","status":"running","sandbox_url":"http://%s"}`, r.Host)
	case r.Method == http.MethodDelete && p == "/sandboxes/sb1":
		f.deleted = true
	case strings.HasPrefix(p, "/api/v1/"):
		if r.Header.Get("X-Tensorlake-Sandbox-Id") != "sb1" {
			f.t.Errorf("proxy request without sandbox id")
		}
		f.proxy(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTL) launch(body io.Reader, pipe bool) (int64, *fakeProc, error) {
	var req struct {
		Command    string            `json:"command"`
		Args       []string          `json:"args"`
		WorkingDir string            `json:"working_dir"`
		Env        map[string]string `json:"env"`
		StdinMode  string            `json:"stdin_mode"`
	}
	_ = json.NewDecoder(body).Decode(&req)
	cmd := exec.Command(req.Command, req.Args...)
	cmd.Dir = req.WorkingDir
	fp := &fakeProc{cmd: cmd}
	fp.cond = sync.NewCond(&fp.mu)
	if pipe || req.StdinMode == "pipe" {
		fp.stdin, _ = cmd.StdinPipe()
	}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	var wg sync.WaitGroup
	scan := func(rd io.Reader, stream string) {
		defer wg.Done()
		sc := bufio.NewScanner(rd)
		for sc.Scan() {
			fp.mu.Lock()
			fp.lines = append(fp.lines, line{sc.Text(), stream})
			fp.cond.Broadcast()
			fp.mu.Unlock()
		}
	}
	wg.Add(2)
	go scan(stdout, "stdout")
	go scan(stderr, "stderr")
	go func() {
		wg.Wait()
		code := 0
		if err := cmd.Wait(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		fp.mu.Lock()
		fp.done, fp.code = true, code
		fp.cond.Broadcast()
		fp.mu.Unlock()
	}()
	pid := int64(cmd.Process.Pid)
	f.mu.Lock()
	f.procs[pid] = fp
	f.mu.Unlock()
	return pid, fp, nil
}

func sse(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", b)
	w.(http.Flusher).Flush()
}

// stream writes a process's lines (optionally one channel) until it ends.
func (fp *fakeProc) stream(w http.ResponseWriter, only string) {
	i := 0
	fp.mu.Lock()
	defer fp.mu.Unlock()
	for {
		for ; i < len(fp.lines); i++ {
			l := fp.lines[i]
			if only == "" || only == l.stream {
				fp.mu.Unlock()
				sse(w, map[string]any{"line": l.text, "stream": l.stream, "timestamp": 0})
				fp.mu.Lock()
			}
		}
		if fp.done {
			return
		}
		fp.cond.Wait()
	}
}

func (f *fakeTL) proxy(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	switch {
	case p == "processes/run":
		pid, fp, err := f.launch(r.Body, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, map[string]any{"pid": pid, "started_at": 0})
		fp.stream(w, "")
		sse(w, map[string]any{"exit_code": fp.code})
	case p == "processes" && r.Method == http.MethodPost:
		pid, _, err := f.launch(r.Body, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"pid":%d,"status":"running","command":"x","started_at":0}`, pid)
	case p == "files":
		path := r.URL.Query().Get("path")
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			if err := os.WriteFile(path, b, 0o644); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		b, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Write(b)
	case strings.HasPrefix(p, "processes/"):
		parts := strings.Split(strings.TrimPrefix(p, "processes/"), "/")
		pid, _ := strconv.ParseInt(parts[0], 10, 64)
		f.mu.Lock()
		fp := f.procs[pid]
		f.mu.Unlock()
		if fp == nil {
			http.NotFound(w, r)
			return
		}
		rest := strings.Join(parts[1:], "/")
		switch {
		case rest == "" && r.Method == http.MethodGet:
			fp.mu.Lock()
			defer fp.mu.Unlock()
			if fp.done {
				fmt.Fprintf(w, `{"pid":%d,"status":"exited","exit_code":%d}`, pid, fp.code)
			} else {
				fmt.Fprintf(w, `{"pid":%d,"status":"running"}`, pid)
			}
		case rest == "" && r.Method == http.MethodDelete:
			fp.cmd.Process.Kill()
		case rest == "stdin":
			b, _ := io.ReadAll(r.Body)
			fp.stdin.Write(b)
		case rest == "stdin/close":
			fp.stdin.Close()
		case strings.HasSuffix(rest, "/follow"):
			w.Header().Set("Content-Type", "text/event-stream")
			only := strings.TrimSuffix(rest, "/follow")
			if only == "output" {
				only = ""
			}
			fp.stream(w, only)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func TestTensorlakeConformance(t *testing.T) {
	f := &fakeTL{t: t, procs: map[int64]*fakeProc{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	box, err := New(Config{APIKey: "k", APIURL: srv.URL, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sandboxtest.RunWith(t, box, sandboxtest.Options{Dir: t.TempDir()})
	net, _ := f.created["network"].(map[string]any)
	if net["allow_internet_access"] != false || f.created["image"] != "tensorlake/ubuntu-minimal" {
		t.Fatalf("create body %v", f.created)
	}
	if f.polls == 0 || !f.deleted {
		t.Fatalf("polls=%d deleted=%v", f.polls, f.deleted)
	}
}

func TestTensorlakeNeedsKey(t *testing.T) {
	t.Setenv("TENSORLAKE_API_KEY", "")
	if _, err := New(Config{}); err == nil {
		t.Fatal("no key accepted")
	}
}

func TestTensorlakeLive(t *testing.T) {
	if os.Getenv("TENSORLAKE_API_KEY") == "" {
		t.Skip("TENSORLAKE_API_KEY not set")
	}
	box, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	sandboxtest.Run(t, box)
}
