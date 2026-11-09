package trace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestOTLPExport(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	echo := kritrim.NewTool("echo", "echo", func(ctx context.Context, in struct{}) (string, error) { return "ok", nil })
	a, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(kritrim.TurnTool("echo", nil), kritrim.Turn{Text: "done"}),
		Tools: []kritrim.Tool{echo},
		Hooks: OTLP(OTLPConfig{Endpoint: srv.URL, Service: "test"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("no OTLP export")
	}
	b, _ := json.Marshal(got)
	s := string(b)
	for _, want := range []string{`"name":"run"`, `"name":"model"`, `"name":"tool echo"`, `"service.name"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
}
