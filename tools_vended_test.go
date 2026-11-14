package kritrim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestHTMLToText(t *testing.T) {
	page := `<!doctype html><html><head><title>Lisbon &amp; Porto</title>
<style>body{color:red}</style></head>
<body><script>var x = "<p>not text</p>";</script>
<h1>Lisbon</h1><p>It is <b>18&deg;C</b>&nbsp;and raining.</p>
<ul><li>Pack a coat</li><li>Pack an umbrella</li></ul>
<!-- a comment --></body></html>`
	got := htmlToText(page)
	for _, want := range []string{"Lisbon & Porto", "It is 18°C and raining.", "Pack a coat", "Pack an umbrella"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"var x", "color:red", "a comment", "<p>", "&deg;"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("kept %q in:\n%s", unwanted, got)
		}
	}
	// Blocks become line breaks rather than running together.
	if !strings.Contains(got, "Pack a coat\nPack an umbrella") {
		t.Errorf("list items ran together:\n%s", got)
	}
}

func TestWebFetch(t *testing.T) {
	if _, err := New(Config{Model: Mock(), Tools: []Tool{WebFetch(WebFetchConfig{})}}); err == nil {
		t.Fatal("WebFetch built with no allowlist")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><body><h1>Hi</h1><p>there</p></body></html>"))
		case "/data":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	host, _, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	ctx := testEnv(t)
	tool := WebFetch(WebFetchConfig{AllowHosts: []string{host}})

	out, err := tool.run(ctx, map[string]any{"url": srv.URL + "/page"})
	if err != nil || !strings.Contains(out, "Hi") || !strings.Contains(out, "there") || strings.Contains(out, "<h1>") {
		t.Fatalf("html: %q %v", out, err)
	}
	// Non-HTML comes back untouched.
	out, err = tool.run(ctx, map[string]any{"url": srv.URL + "/data"})
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("json: %q %v", out, err)
	}
	// A non-2xx is an error, not a body.
	if _, err := tool.run(ctx, map[string]any{"url": srv.URL + "/missing"}); err == nil {
		t.Fatal("404 accepted")
	}
	// Off-allowlist and non-http are refused.
	if _, err := tool.run(ctx, map[string]any{"url": "https://example.com/"}); err == nil {
		t.Fatal("off-allowlist host fetched")
	}
	if _, err := tool.run(ctx, map[string]any{"url": "file:///etc/passwd"}); err == nil {
		t.Fatal("file scheme fetched")
	}
	// Truncation is honoured.
	short := WebFetch(WebFetchConfig{AllowHosts: []string{host}, MaxChars: 4})
	out, err = short.run(ctx, map[string]any{"url": srv.URL + "/data"})
	if err != nil || len([]rune(out)) > 5 {
		t.Fatalf("truncate: %q %v", out, err)
	}
}

func TestTodos(t *testing.T) {
	ctx := context.WithValue(context.Background(), keyState, newState(nil))
	tool := Todos()
	if out, err := tool.run(ctx, map[string]any{"op": "list"}); err != nil || out != "no todos yet" {
		t.Fatalf("empty list: %q %v", out, err)
	}
	items := []any{
		map[string]any{"task": "read the code", "status": "done"},
		map[string]any{"task": "write the test", "status": "in_progress"},
		map[string]any{"task": "ship", "status": "pending"},
	}
	out, err := tool.run(ctx, map[string]any{"op": "write", "items": items})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[x] read the code") || !strings.Contains(out, "[~] write the test") ||
		!strings.Contains(out, "[ ] ship") || !strings.Contains(out, "1/3 done") {
		t.Fatalf("rendered:\n%s", out)
	}
	// The list survives as plain JSON types, so any session store keeps it.
	again, err := tool.run(ctx, map[string]any{"op": "list"})
	if err != nil || again != out {
		t.Fatalf("list after write: %q != %q (%v)", again, out, err)
	}
	// Two in_progress items are refused: finish one first.
	_, err = tool.run(ctx, map[string]any{"op": "write", "items": []any{
		map[string]any{"task": "a", "status": "in_progress"},
		map[string]any{"task": "b", "status": "in_progress"},
	}})
	if err == nil || !strings.Contains(err.Error(), "in_progress") {
		t.Fatalf("want one-in-progress error, got %v", err)
	}
	// A bad status and an empty task are refused.
	for _, bad := range []any{
		[]any{map[string]any{"task": "a", "status": "later"}},
		[]any{map[string]any{"task": "  ", "status": "pending"}},
	} {
		if _, err := tool.run(ctx, map[string]any{"op": "write", "items": bad}); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	// The earlier good list is still there: a rejected write changes nothing.
	if cur, _ := tool.run(ctx, map[string]any{"op": "list"}); cur != out {
		t.Fatalf("list changed after a rejected write:\n%s", cur)
	}
}

// TestTodosSurviveASession checks the plan is still there after a pause and
// a reload, which is the reason it lives in session state.
func TestTodosSurviveASession(t *testing.T) {
	a := newAgent(t, Config{
		Model: Mock(TurnTool("todos", map[string]any{"op": "write", "items": []any{
			map[string]any{"task": "step one", "status": "pending"}}}), Turn{Text: "planned"}),
		Tools:   []Tool{Todos()},
		Session: MemorySessions(),
	})
	if _, err := a.Run(context.Background(), "plan it", Thread("t1")); err != nil {
		t.Fatal(err)
	}
	sess, err := a.Session(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	list, _ := sess.Values["todos"].([]any)
	if len(list) != 1 {
		t.Fatalf("stored todos = %#v", sess.Values["todos"])
	}
}

func TestPythonNeedsASandbox(t *testing.T) {
	ctx := testEnv(t) // a host env with no sandbox machine
	_, err := Python(PythonConfig{}).run(ctx, map[string]any{"code": "print(1)"})
	if err == nil || !strings.Contains(err.Error(), "needs a sandbox") {
		t.Fatalf("want sandbox refusal, got %v", err)
	}
	// Empty code is refused before anything runs.
	if _, err := Python(PythonConfig{AllowUnsafeHost: true}).run(ctx, map[string]any{"code": " "}); err == nil {
		t.Fatal("empty code accepted")
	}
	// The tool is risky, so it cannot run without an approver.
	if !Python(PythonConfig{}).risky() {
		t.Fatal("python is not marked risky")
	}
}

// TestPythonRunsInASandbox uses the local sandbox as a stand-in for a
// container: the point is that Exec goes through the machine and the code
// arrives on stdin, never as an argument.
func TestPythonRunsInASandbox(t *testing.T) {
	if _, err := lookPython(); err != nil {
		t.Skip("no python3 on this machine")
	}
	a := newAgent(t, Config{Model: Mock(), Sandbox: Local(),
		Tools: []Tool{Python(PythonConfig{})}})
	ctx := context.WithValue(context.Background(), keyEnv, a.env)
	out, err := Python(PythonConfig{}).run(ctx, map[string]any{"code": "print(6 * 7)"})
	if err != nil || strings.TrimSpace(out) != "42" {
		t.Fatalf("%q %v", out, err)
	}
	// A failing program returns its message and a non-nil error.
	out, err = Python(PythonConfig{}).run(ctx, map[string]any{"code": "raise SystemExit(3)"})
	if err == nil {
		t.Fatalf("exit 3 reported as success: %q", out)
	}
}

func lookPython() (string, error) { return exec.LookPath("python3") }
