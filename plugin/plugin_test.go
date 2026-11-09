package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestInject(t *testing.T) {
	m := &spyModel{}
	a, err := kritrim.New(kritrim.Config{Model: m, Hooks: Inject(func(ctx context.Context) string {
		return "current time: noon"
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.reqs[0].System, "current time: noon") {
		t.Fatalf("system: %q", m.reqs[0].System)
	}
}

type spyModel struct{ reqs []kritrim.Request }

func (m *spyModel) Generate(ctx context.Context, req kritrim.Request) (*kritrim.Response, error) {
	m.reqs = append(m.reqs, req)
	return &kritrim.Response{Text: "ok", Stop: "stop"}, nil
}
func (m *spyModel) Stream(ctx context.Context, req kritrim.Request, emit func(kritrim.Delta)) (*kritrim.Response, error) {
	return m.Generate(ctx, req)
}

func TestOffload(t *testing.T) {
	dir := t.TempDir()
	hooks, tool := Offload(dir, 100)
	big := strings.Repeat("x", 500)
	call := kritrim.ToolCall{Name: "big"}
	res := kritrim.ToolResult{Name: "big", Content: big}
	hooks.AfterTool(context.Background(), &kritrim.ToolEvent{Call: &call, Result: &res})
	if !strings.HasPrefix(res.Content, "[offloaded") {
		t.Fatalf("%q", res.Content)
	}
	name := strings.Fields(res.Content)[2]
	name = strings.TrimSuffix(name, ";")
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(b) != big {
		t.Fatalf("offload file %v", err)
	}
	a, err := kritrim.New(kritrim.Config{Model: kritrim.Mock(), Tools: []kritrim.Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	got, err := a.CallTool(context.Background(), "read_offloaded", map[string]any{"path": name})
	if err != nil || got != big {
		t.Fatalf("read %d %v", len(got), err)
	}
}
