package kritrim

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

type spyModel struct {
	mu    sync.Mutex
	reqs  []Request
	turns []Turn
}

func (m *spyModel) next() (*Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) == 0 {
		return &Response{Text: "done", Stop: "stop"}, nil
	}
	t := m.turns[0]
	if len(m.turns) > 1 {
		m.turns = m.turns[1:]
	}
	stop := t.Stop
	if stop == "" {
		if len(t.ToolCalls) > 0 {
			stop = "tool_calls"
		} else {
			stop = "stop"
		}
	}
	return &Response{Text: t.Text, ToolCalls: t.ToolCalls, Usage: t.Usage, Stop: stop}, t.Err
}

func (m *spyModel) Generate(ctx context.Context, req Request) (*Response, error) {
	m.mu.Lock()
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()
	return m.next()
}

func (m *spyModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	m.mu.Lock()
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()
	resp, err := m.next()
	if err == nil && resp.Text != "" {
		emit(Delta{Text: resp.Text})
	}
	return resp, err
}

func newAgent(t *testing.T, cfg Config) *Agent {
	t.Helper()
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func TestRunToolAndAnswer(t *testing.T) {
	echo := NewTool("echo", "echo text", func(ctx context.Context, in struct {
		Text string `json:"text" desc:"text to echo"`
	}) (string, error) {
		return in.Text, nil
	})
	m := &spyModel{turns: []Turn{TurnTool("echo", map[string]any{"text": "hi"}), {Text: "done"}}}
	a := newAgent(t, Config{Model: m, Tools: []Tool{echo}})
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" || len(res.Steps) != 2 {
		t.Fatalf("got %+v", res)
	}
	if got := res.Steps[0].ToolResults[0].Content; got != "hi" {
		t.Fatalf("tool result %q", got)
	}
	if m.reqs[1].Messages[len(m.reqs[1].Messages)-1].Role != RoleTool {
		t.Fatalf("tool result not sent to model")
	}
}

func TestThreadPersists(t *testing.T) {
	m := &spyModel{turns: []Turn{{Text: "one"}, {Text: "two"}}}
	a := newAgent(t, Config{Model: m})
	ctx := context.Background()
	if _, err := a.Run(ctx, "first", Thread("t1")); err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(ctx, "second", Thread("t1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Thread != "t1" {
		t.Fatalf("thread %q", res.Thread)
	}
	last := m.reqs[1].Messages
	if len(last) != 3 || last[0].Content != "first" || last[2].Content != "second" {
		t.Fatalf("history not carried: %+v", last)
	}
}

func TestOutputRepairsOnce(t *testing.T) {
	type recipe struct {
		Name string `json:"name"`
	}
	m := &spyModel{turns: []Turn{{Text: "not json"}, {Text: `{"name":"pancakes"}`}}}
	a := newAgent(t, Config{Model: m})
	var r recipe
	res, err := a.Run(context.Background(), "recipe", Output(&r))
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "pancakes" || len(m.reqs) != 2 {
		t.Fatalf("got %+v after %d calls", r, len(m.reqs))
	}
	if m.reqs[0].Schema == nil {
		t.Fatal("schema not sent")
	}
	if res.Text == "" {
		t.Fatal("result text empty")
	}
}

func TestOutputFails(t *testing.T) {
	m := &spyModel{turns: []Turn{{Text: "nope"}, {Text: "still nope"}}}
	a := newAgent(t, Config{Model: m})
	var r struct{ X int }
	if _, err := a.Run(context.Background(), "x", Output(&r)); !errors.Is(err, ErrOutput) {
		t.Fatalf("want ErrOutput, got %v", err)
	}
}

func TestApproval(t *testing.T) {
	danger := Risky(NewTool("danger", "danger", func(ctx context.Context, in struct{}) (string, error) {
		return "ran", nil
	}))
	var asked int
	m := &spyModel{turns: []Turn{TurnTool("danger", nil), {Text: "ok"}}}
	a := newAgent(t, Config{Model: m, Tools: []Tool{danger},
		Approve: func(ctx context.Context, req Approval) (Decision, error) {
			asked++
			if req.Tool != "danger" {
				t.Fatalf("tool %q", req.Tool)
			}
			return Approve(), nil
		}})
	if _, err := a.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Fatalf("asked %d", asked)
	}
}

func TestApprovalRejected(t *testing.T) {
	danger := Risky(NewTool("danger", "danger", func(ctx context.Context, in struct{}) (string, error) {
		t.Fatal("tool must not run")
		return "", nil
	}))
	m := &spyModel{turns: []Turn{TurnTool("danger", nil), {Text: "ok"}}}
	a := newAgent(t, Config{Model: m, Tools: []Tool{danger},
		Approve: func(ctx context.Context, req Approval) (Decision, error) { return Reject("no"), nil }})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Steps[0].ToolResults[0].Content, "rejected") {
		t.Fatalf("got %q", res.Steps[0].ToolResults[0].Content)
	}
}

func TestStreamEvents(t *testing.T) {
	a := newAgent(t, Config{Model: Mock(Turn{Text: "hello"})})
	var types []string
	for e, err := range a.Stream(context.Background(), "hi") {
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, e.Type)
	}
	want := map[string]bool{"step_start": false, "text": false, "step_end": false, "finish": false}
	for _, ty := range types {
		if _, ok := want[ty]; ok {
			want[ty] = true
		}
	}
	for ty, seen := range want {
		if !seen {
			t.Fatalf("missing event %q in %v", ty, types)
		}
	}
}

func TestLimits(t *testing.T) {
	tool := NewTool("loop", "loop", func(ctx context.Context, in struct{}) (string, error) { return "again", nil })
	m := &spyModel{turns: []Turn{TurnTool("loop", nil)}}
	a := newAgent(t, Config{Model: m, Tools: []Tool{tool}, Limits: Limits{Steps: 2}})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != "max_steps" {
		t.Fatalf("stop %q", res.Stop)
	}
}

func TestTokenBudget(t *testing.T) {
	m := &spyModel{turns: []Turn{{Text: "x", Usage: Usage{In: 10, Out: 10}}}}
	a := newAgent(t, Config{Model: m, Limits: Limits{Tokens: 5}})
	if _, err := a.Run(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "token budget") {
		t.Fatalf("got %v", err)
	}
}

func TestFileSessions(t *testing.T) {
	dir := t.TempDir()
	m := &spyModel{turns: []Turn{{Text: "a"}, {Text: "b"}}}
	a := newAgent(t, Config{Model: m, Session: FileSessions(dir)})
	ctx := context.Background()
	if _, err := a.Run(ctx, "one", Thread("u/1")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("session file: %v %v", entries, err)
	}
	if _, err := a.Run(ctx, "two", Thread("u/1")); err != nil {
		t.Fatal(err)
	}
	if len(m.reqs[1].Messages) != 3 {
		t.Fatalf("history %+v", m.reqs[1].Messages)
	}
}

func TestValidation(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("want error for missing model")
	}
	bad := NewTool("bad name!", "x", func(ctx context.Context, in struct{}) (string, error) { return "", nil })
	if _, err := New(Config{Model: Mock(), Tools: []Tool{bad}}); err == nil {
		t.Fatal("want error for bad tool name")
	}
	dup := NewTool("same", "x", func(ctx context.Context, in struct{}) (string, error) { return "", nil })
	if _, err := New(Config{Model: Mock(), Tools: []Tool{dup, dup}}); err == nil {
		t.Fatal("want error for duplicate tool")
	}
}
