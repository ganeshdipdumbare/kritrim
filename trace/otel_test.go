package trace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestOTelMetricsAndLogs(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v any
		_ = json.NewDecoder(r.Body).Decode(&v)
		b, _ := json.Marshal(v)
		mu.Lock()
		bodies[r.URL.Path] = string(b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	echo := kritrim.NewTool("echo", "echo", func(ctx context.Context, in struct{}) (string, error) { return "ok", nil })
	a, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(kritrim.TurnTool("echo", nil), kritrim.Turn{Text: "done"}),
		Tools: []kritrim.Tool{echo},
		Hooks: OTel(OTLPConfig{Endpoint: srv.URL, Service: "test"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range []string{"/v1/traces", "/v1/metrics", "/v1/logs"} {
		if bodies[p] == "" {
			t.Fatalf("no %s export", p)
		}
	}
	for _, want := range []string{"kritrim.model.calls", "kritrim.model.tokens", "kritrim.tool.calls", "kritrim.runs"} {
		if !strings.Contains(bodies["/v1/metrics"], want) {
			t.Fatalf("metrics missing %s: %s", want, bodies["/v1/metrics"])
		}
	}
	if !strings.Contains(bodies["/v1/logs"], "run.end") || !strings.Contains(bodies["/v1/logs"], "tool") {
		t.Fatalf("logs: %s", bodies["/v1/logs"])
	}
	if !strings.Contains(bodies["/v1/traces"], `"name":"run"`) {
		t.Fatalf("traces: %s", bodies["/v1/traces"])
	}
}
