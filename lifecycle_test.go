package kritrim

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParallelTools(t *testing.T) {
	slow := func(name string) Tool {
		return NewTool(name, name, func(ctx context.Context, in struct{}) (string, error) {
			time.Sleep(120 * time.Millisecond)
			return name, nil
		})
	}
	m := &spyModel{turns: []Turn{{ToolCalls: []ToolCall{{ID: "a", Name: "slow1"}, {ID: "b", Name: "slow2"}}}, {Text: "done"}}}
	a := newAgent(t, Config{Model: m, Tools: []Tool{slow("slow1"), slow("slow2")}, ToolConcurrency: 2})
	start := time.Now()
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 220*time.Millisecond {
		t.Fatalf("took %v; not parallel", elapsed)
	}
	if len(res.Steps[0].ToolResults) != 2 || res.Steps[0].ToolResults[1].Content != "slow2" {
		t.Fatalf("%+v", res.Steps[0].ToolResults)
	}
}

func TestCallToolDirect(t *testing.T) {
	echo := NewTool("echo", "echo", func(ctx context.Context, in struct {
		Text string `json:"text"`
	}) (string, error) {
		return in.Text, nil
	})
	a := newAgent(t, Config{Model: Mock(), Tools: []Tool{echo}})
	got, err := a.CallTool(context.Background(), "echo", map[string]any{"text": "hi"})
	if err != nil || got != "hi" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := a.CallTool(context.Background(), "missing", nil); err == nil {
		t.Fatal("unknown tool should fail")
	}
}

func TestForkSession(t *testing.T) {
	m := &spyModel{turns: []Turn{{Text: "one"}}}
	a := newAgent(t, Config{Model: m})
	ctx := context.Background()
	if _, err := a.Run(ctx, "hello", Thread("a")); err != nil {
		t.Fatal(err)
	}
	if err := a.Fork(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	sess, err := a.Session(ctx, "b")
	if err != nil || sess == nil || len(sess.Messages) != 2 {
		t.Fatalf("%+v %v", sess, err)
	}
	if err := a.Fork(ctx, "a", "b"); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

type blockingModel struct{ started chan struct{} }

func (m *blockingModel) Generate(ctx context.Context, req Request) (*Response, error) {
	return m.Stream(ctx, req, nil)
}
func (m *blockingModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	if m.started != nil {
		close(m.started)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCancelRun(t *testing.T) {
	started := make(chan struct{})
	m := &blockingModel{started: started}
	a := newAgent(t, Config{Model: m})
	done := make(chan error, 1)
	go func() {
		_, err := a.Run(context.Background(), "x", RunID("r1"))
		done <- err
	}()
	<-started
	if !a.Cancel("r1") {
		t.Fatal("run not registered")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not stop")
	}
	if a.Cancel("r1") {
		t.Fatal("cancel should not find a finished run")
	}
}

type countingModel struct{ n atomic.Int32 }

func (m *countingModel) Generate(ctx context.Context, req Request) (*Response, error) {
	m.n.Add(1)
	time.Sleep(50 * time.Millisecond)
	return &Response{Text: "ok", Stop: "stop"}, nil
}
func (m *countingModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	return m.Generate(ctx, req)
}

func TestIdempotency(t *testing.T) {
	m := &countingModel{}
	a := newAgent(t, Config{Model: m})
	var wg sync.WaitGroup
	results := make([]*Result, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = a.Run(context.Background(), "x", Idempotency("k"))
		}(i)
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		if errs[i] != nil || results[i] == nil || results[i].Text != "ok" {
			t.Fatalf("%d: %+v %v", i, results[i], errs[i])
		}
	}
	if m.n.Load() != 1 {
		t.Fatalf("model calls %d", m.n.Load())
	}
}

func TestNewRawTool(t *testing.T) {
	tool := NewRawTool("raw", "raw", objectSchema(map[string]any{"x": map[string]any{"type": "string"}}),
		func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil })
	a := newAgent(t, Config{Model: Mock(), Tools: []Tool{tool}})
	got, err := a.CallTool(context.Background(), "raw", map[string]any{"x": "1"})
	if err != nil || got != "ok" {
		t.Fatalf("%q %v", got, err)
	}
}
