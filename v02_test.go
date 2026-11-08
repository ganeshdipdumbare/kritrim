package kritrim

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type flakyModel struct {
	mu    sync.Mutex
	calls int
	fail  int
	resp  *Response
}

func (m *flakyModel) next() (*Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls <= m.fail {
		return nil, &StatusError{Provider: "test", Code: 429, Status: "429 Too Many Requests", Body: "slow down"}
	}
	return m.resp, nil
}

func (m *flakyModel) Generate(ctx context.Context, req Request) (*Response, error) { return m.next() }
func (m *flakyModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	resp, err := m.next()
	if err == nil {
		emit(Delta{Text: resp.Text})
	}
	return resp, err
}

func TestDurablePauseResume(t *testing.T) {
	dir := t.TempDir()
	var ran atomic.Int32
	refund := Risky(NewTool("refund", "refund", func(ctx context.Context, in struct {
		Order string `json:"order"`
	}) (string, error) {
		ran.Add(1)
		return "refunded " + in.Order, nil
	}))

	first := &spyModel{turns: []Turn{TurnTool("refund", map[string]any{"order": "A-17"})}}
	a1 := newAgent(t, Config{
		Model:   first,
		Tools:   []Tool{refund},
		Session: FileSessions(dir),
		Approve: func(ctx context.Context, req Approval) (Decision, error) { return Pause(), nil },
	})
	res, err := a1.Run(context.Background(), "refund A-17", Thread("t1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != "interrupt" || len(res.Interrupts) != 1 {
		t.Fatalf("stop=%q interrupts=%+v", res.Stop, res.Interrupts)
	}
	if ran.Load() != 0 {
		t.Fatal("tool ran before approval")
	}

	second := &spyModel{turns: []Turn{{Text: "done"}}}
	a2 := newAgent(t, Config{Model: second, Tools: []Tool{refund}, Session: FileSessions(dir)})
	res2, err := a2.Run(context.Background(), "", Thread("t1"),
		Decisions(map[string]Decision{res.Interrupts[0].ID: Approve()}))
	if err != nil {
		t.Fatal(err)
	}
	if res2.Stop != "stop" || res2.Text != "done" {
		t.Fatalf("%+v", res2)
	}
	if ran.Load() != 1 {
		t.Fatalf("tool ran %d times", ran.Load())
	}
	if got := res2.Steps[0].ToolResults[0].Content; got != "refunded A-17" {
		t.Fatalf("result %q", got)
	}
}

func TestDurablePauseReject(t *testing.T) {
	dir := t.TempDir()
	var ran atomic.Int32
	refund := Risky(NewTool("refund", "refund", func(ctx context.Context, in struct{}) (string, error) {
		ran.Add(1)
		return "ok", nil
	}))
	first := &spyModel{turns: []Turn{TurnTool("refund", nil)}}
	a1 := newAgent(t, Config{Model: first, Tools: []Tool{refund}, Session: FileSessions(dir),
		Approve: func(ctx context.Context, req Approval) (Decision, error) { return Pause(), nil }})
	res, err := a1.Run(context.Background(), "x", Thread("t1"))
	if err != nil || res.Stop != "interrupt" {
		t.Fatalf("%v %+v", err, res)
	}
	second := &spyModel{turns: []Turn{{Text: "ok then"}}}
	a2 := newAgent(t, Config{Model: second, Tools: []Tool{refund}, Session: FileSessions(dir)})
	res2, err := a2.Run(context.Background(), "", Thread("t1"),
		Decisions(map[string]Decision{res.Interrupts[0].ID: Reject("no")}))
	if err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 0 || !strings.Contains(res2.Steps[0].ToolResults[0].Content, "rejected") {
		t.Fatalf("ran=%d result=%q", ran.Load(), res2.Steps[0].ToolResults[0].Content)
	}
}

func TestPauseNeedsThread(t *testing.T) {
	refund := Risky(NewTool("refund", "refund", func(ctx context.Context, in struct{}) (string, error) { return "", nil }))
	a := newAgent(t, Config{Model: &spyModel{turns: []Turn{TurnTool("refund", nil)}}, Tools: []Tool{refund},
		Approve: func(ctx context.Context, req Approval) (Decision, error) { return Pause(), nil }})
	if _, err := a.Run(context.Background(), "x"); err == nil {
		t.Fatal("want durable pause error without a thread")
	}
}

func TestResumeWithoutDecisionsReturnsPending(t *testing.T) {
	dir := t.TempDir()
	refund := Risky(NewTool("refund", "refund", func(ctx context.Context, in struct{}) (string, error) { return "ok", nil }))
	a1 := newAgent(t, Config{Model: &spyModel{turns: []Turn{TurnTool("refund", nil)}}, Tools: []Tool{refund},
		Session: FileSessions(dir), Approve: func(ctx context.Context, req Approval) (Decision, error) { return Pause(), nil }})
	res, err := a1.Run(context.Background(), "x", Thread("t"))
	if err != nil {
		t.Fatal(err)
	}
	a2 := newAgent(t, Config{Model: Mock(), Tools: []Tool{refund}, Session: FileSessions(dir)})
	res2, err := a2.Run(context.Background(), "", Thread("t"))
	if err != nil {
		t.Fatal(err)
	}
	if res2.Stop != "interrupt" || len(res2.Interrupts) != 1 || res2.Interrupts[0].ID != res.Interrupts[0].ID {
		t.Fatalf("%+v", res2)
	}
}

func TestRetryOnRateLimit(t *testing.T) {
	m := &flakyModel{fail: 1, resp: &Response{Text: "ok", Stop: "stop"}}
	a := newAgent(t, Config{Model: m, Retry: &Retry{Max: 3, Base: time.Millisecond}})
	res, err := a.Run(context.Background(), "x")
	if err != nil || res.Text != "ok" {
		t.Fatalf("%v %+v", err, res)
	}
	if m.calls != 2 {
		t.Fatalf("calls %d", m.calls)
	}
}

func TestHooksDenyAndObserve(t *testing.T) {
	tool := NewTool("danger", "danger", func(ctx context.Context, in struct{}) (string, error) {
		t.Fatal("denied tool ran")
		return "", nil
	})
	m := &spyModel{turns: []Turn{TurnTool("danger", nil), {Text: "ok"}}}
	var denied int
	a := newAgent(t, Config{Model: m, Tools: []Tool{tool}, Hooks: Hooks{
		BeforeTool: func(ctx context.Context, e *ToolEvent) error { denied++; e.Deny = "policy"; return nil },
	}})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if denied != 1 || !strings.Contains(res.Steps[0].ToolResults[0].Content, "blocked") {
		t.Fatalf("%+v %q", res, res.Steps[0].ToolResults[0].Content)
	}
}

func TestContextTrimsHistory(t *testing.T) {
	store := MemorySessions()
	ctx := context.Background()
	msgs := make([]Message, 0, 100)
	for i := 0; i < 100; i++ {
		msgs = append(msgs, Message{Role: RoleUser, Content: strings.Repeat("word ", 40)})
	}
	if err := store.Save(ctx, &Session{ID: "t", Messages: msgs}); err != nil {
		t.Fatal(err)
	}
	m := &spyModel{turns: []Turn{{Text: "ok"}}}
	a := newAgent(t, Config{Model: m, Session: store, Context: &ContextConfig{MaxTokens: 200, KeepLast: 4}})
	if _, err := a.Run(ctx, "next", Thread("t")); err != nil {
		t.Fatal(err)
	}
	if got := len(m.reqs[0].Messages); got > 5 {
		t.Fatalf("request kept %d messages", got)
	}
}

func TestStatePersists(t *testing.T) {
	dir := t.TempDir()
	track := NewTool("track", "track", func(ctx context.Context, in struct{}) (string, error) {
		StateFrom(ctx).Set("seen", "yes")
		return "ok", nil
	})
	m := &spyModel{turns: []Turn{TurnTool("track", nil), {Text: "done"}}}
	a := newAgent(t, Config{Model: m, Tools: []Tool{track}, Session: FileSessions(dir)})
	if _, err := a.Run(context.Background(), "x", Thread("t")); err != nil {
		t.Fatal(err)
	}
	sess, err := FileSessions(dir).Load(context.Background(), "t")
	if err != nil || sess == nil || sess.Values["seen"] != "yes" {
		t.Fatalf("%+v %v", sess, err)
	}
}

func TestSessionConflict(t *testing.T) {
	store := MemorySessions()
	ctx := context.Background()
	if err := store.Save(ctx, &Session{ID: "s", Messages: []Message{{Role: RoleUser, Content: "a"}}}); err != nil {
		t.Fatal(err)
	}
	a, _ := store.Load(ctx, "s")
	b, _ := store.Load(ctx, "s")
	a.Messages = append(a.Messages, Message{Role: RoleAssistant, Content: "1"})
	if err := store.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	b.Messages = append(b.Messages, Message{Role: RoleAssistant, Content: "2"})
	if err := store.Save(ctx, b); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestAgentAsTool(t *testing.T) {
	inner := newAgent(t, Config{Model: Mock(Turn{Text: "inner answer"})})
	outer := newAgent(t, Config{
		Model: &spyModel{turns: []Turn{TurnTool("expert", map[string]any{"task": "q"}), {Text: "ok"}}},
		Tools: []Tool{inner.AsTool("expert", "an expert")},
	})
	res, err := outer.Run(context.Background(), "ask")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Steps[0].ToolResults[0].Content; got != "inner answer" {
		t.Fatalf("subagent result %q", got)
	}
}
