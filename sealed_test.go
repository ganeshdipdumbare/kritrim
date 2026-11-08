package kritrim

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

func TestPeerRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	pa, pb := newPeer(a, a), newPeer(b, b)
	pa.handle("ping", func(context.Context, json.RawMessage) (any, error) {
		return map[string]string{"pong": "a"}, nil
	})
	pb.handle("ping", func(context.Context, json.RawMessage) (any, error) {
		return map[string]string{"pong": "b"}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out map[string]string
	if err := pa.call(ctx, "ping", nil, &out); err != nil || out["pong"] != "b" {
		t.Fatalf("%v %+v", err, out)
	}
	if err := pb.call(ctx, "ping", nil, &out); err != nil || out["pong"] != "a" {
		t.Fatalf("%v %+v", err, err)
	}
}

func TestBoxRunOverRPC(t *testing.T) {
	hostSide, boxSide := net.Pipe()
	defer hostSide.Close()
	defer boxSide.Close()

	var (
		mu     sync.Mutex
		events int
		calls  int
	)
	cfg := Config{Model: Mock(Turn{Text: "unused"})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeBox(ctx, boxSide, boxSide, cfg) }()

	host := newPeer(hostSide, hostSide)
	host.handle("ping", func(context.Context, json.RawMessage) (any, error) { return map[string]bool{"ok": true}, nil })
	host.handle("event", func(_ context.Context, raw json.RawMessage) (any, error) {
		mu.Lock()
		events++
		mu.Unlock()
		return nil, nil
	})
	host.handle("model", func(_ context.Context, raw json.RawMessage) (any, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return &Response{Text: "hi from host", Stop: "stop"}, nil
	})

	callCtx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	var res Result
	if err := host.call(callCtx, "run.start", runParams{Prompt: "hello"}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Text != "hi from host" || res.Stop != "stop" {
		t.Fatalf("%+v", res)
	}
	if calls != 1 {
		t.Fatalf("model calls %d", calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if events == 0 {
		t.Fatal("no events streamed")
	}
}

func TestSealedBridgedTool(t *testing.T) {
	hostSide, boxSide := net.Pipe()
	defer hostSide.Close()
	defer boxSide.Close()

	var toolCalls int
	hostTool := Tool{Name: "host_tool", Description: "runs on the host",
		Schema: objectSchema(map[string]any{}),
		run: func(context.Context, map[string]any) (string, error) {
			toolCalls++
			return "host-result", nil
		}}
	cfg := Config{
		Model:  Mock(),
		Tools:  []Tool{hostTool},
		Sealed: &Sealed{Bridged: []string{"host_tool"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeBox(ctx, boxSide, boxSide, cfg) }()

	host := newPeer(hostSide, hostSide)
	host.handle("ping", func(context.Context, json.RawMessage) (any, error) { return map[string]bool{"ok": true}, nil })
	n := 0
	host.handle("model", func(context.Context, json.RawMessage) (any, error) {
		n++
		if n == 1 {
			return &Response{Stop: "tool_calls", ToolCalls: []ToolCall{{ID: "c1", Name: "host_tool"}}}, nil
		}
		return &Response{Text: "done", Stop: "stop"}, nil
	})
	host.handle("tool", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Name string
			Args map[string]any
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.Name != "host_tool" {
			t.Errorf("tool %q", p.Name)
		}
		return hostTool.run(context.Background(), p.Args)
	})
	host.handle("event", func(context.Context, json.RawMessage) (any, error) { return nil, nil })

	callCtx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	var res Result
	if err := host.call(callCtx, "run.start", runParams{Prompt: "x"}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" || toolCalls != 1 {
		t.Fatalf("%+v calls=%d", res, toolCalls)
	}
	if got := res.Steps[0].ToolResults[0].Content; got != "host-result" {
		t.Fatalf("tool result %q", got)
	}
}

func TestSealedSubstitute(t *testing.T) {
	b := &boxHost{secrets: map[string]secretBinding{
		"ph1": {value: "secret-value", hosts: []string{"api.example.com"}},
	}}
	ok := httpRequest{URL: "https://api.example.com/v1?tok=ph1", Headers: map[string]string{"Authorization": "Bearer ph1"}}
	if err := b.substitute(&ok); err != nil {
		t.Fatal(err)
	}
	if ok.URL != "https://api.example.com/v1?tok=secret-value" || ok.Headers["Authorization"] != "Bearer secret-value" {
		t.Fatalf("%+v", ok)
	}
	bad := httpRequest{URL: "https://evil.example.com/?tok=ph1"}
	if err := b.substitute(&bad); err == nil {
		t.Fatal("secret leaked to a host it is not allowed for")
	}
}
