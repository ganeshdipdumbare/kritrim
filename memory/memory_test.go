package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestStore(t *testing.T) {
	ctx := context.Background()
	for _, s := range []Store{NewMemory(), mustFile(t)} {
		if err := s.Put(ctx, "city", "Lisbon"); err != nil {
			t.Fatal(err)
		}
		if v, ok := s.Get(ctx, "city"); !ok || v != "Lisbon" {
			t.Fatalf("%q %v", v, ok)
		}
		items, err := s.Search(ctx, "lis", 5)
		if err != nil || len(items) != 1 {
			t.Fatalf("%+v %v", items, err)
		}
	}
}

func mustFile(t *testing.T) Store {
	t.Helper()
	s, err := NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInject(t *testing.T) {
	s := NewMemory()
	_ = s.Put(context.Background(), "city", "Lisbon")
	var seen kritrim.Request
	model := modelFunc(func(ctx context.Context, req kritrim.Request) (*kritrim.Response, error) {
		seen = req
		return &kritrim.Response{Text: "ok", Stop: "stop"}, nil
	})
	a, err := kritrim.New(kritrim.Config{Model: model, Hooks: Inject(s, 5)})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Run(context.Background(), "tell me about the city"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen.System, "Lisbon") {
		t.Fatalf("system %q", seen.System)
	}
}

type modelFunc func(context.Context, kritrim.Request) (*kritrim.Response, error)

func (f modelFunc) Generate(ctx context.Context, req kritrim.Request) (*kritrim.Response, error) {
	return f(ctx, req)
}
func (f modelFunc) Stream(ctx context.Context, req kritrim.Request, emit func(kritrim.Delta)) (*kritrim.Response, error) {
	return f(ctx, req)
}

func TestTools(t *testing.T) {
	s := NewMemory()
	agent, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(
			kritrim.TurnTool("remember", map[string]any{"key": "city", "value": "Lisbon"}),
			kritrim.TurnTool("recall", map[string]any{"query": "city"}),
			kritrim.Turn{Text: "done"},
		),
		Tools: Tools(s),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	res, err := agent.Run(context.Background(), "remember")
	if err != nil {
		t.Fatal(err)
	}
	results := res.Steps[0].ToolResults
	if len(results) != 1 || !strings.Contains(results[0].Content, "remembered") {
		t.Fatalf("%+v", results)
	}
	if got := res.Steps[1].ToolResults[0].Content; !strings.Contains(got, "Lisbon") {
		t.Fatalf("%q", got)
	}
}
