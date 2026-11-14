package kritrim

import (
	"context"
	"sync/atomic"
	"testing"
)

func echoTool(name string, calls *atomic.Int32) Tool {
	return NewTool(name, "echo", func(ctx context.Context, in struct {
		Text string `json:"text"`
	}) (string, error) {
		if calls != nil {
			calls.Add(1)
		}
		return name + ":" + in.Text, nil
	})
}

func TestHookRetryCallsTheModelAgain(t *testing.T) {
	var seen int
	a := newAgent(t, Config{
		Model: Mock(Turn{Text: "bad"}, Turn{Text: "good"}),
		Hooks: Hooks{AfterModel: func(ctx context.Context, e *ModelEvent) {
			seen++
			e.Retry = e.Response.Text == "bad"
		}},
	})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "good" || seen != 2 {
		t.Fatalf("text=%q seen=%d", res.Text, seen)
	}
}

func TestHookRetryIsCapped(t *testing.T) {
	var seen int
	a := newAgent(t, Config{
		Model: Mock(Turn{Text: "bad"}),
		Retry: &Retry{Max: 2},
		Hooks: Hooks{AfterModel: func(ctx context.Context, e *ModelEvent) {
			seen++
			e.Retry = true
		}},
	})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if seen != 3 || res.Text != "bad" {
		t.Fatalf("seen=%d text=%q, want 3 calls (1 + 2 retries)", seen, res.Text)
	}
}

func TestHookEndAfterModelDropsToolCalls(t *testing.T) {
	var calls atomic.Int32
	a := newAgent(t, Config{
		Model: Mock(Turn{Text: "about to call", ToolCalls: []ToolCall{{ID: "c1", Name: "echo", Args: map[string]any{"text": "x"}}}}),
		Tools: []Tool{echoTool("echo", &calls)},
		Hooks: Hooks{AfterModel: func(ctx context.Context, e *ModelEvent) { e.End = "enough" }},
	})
	res, err := a.Run(context.Background(), "x", Thread("t"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopHook || calls.Load() != 0 {
		t.Fatalf("stop=%q calls=%d", res.Stop, calls.Load())
	}
	last := res.Messages[len(res.Messages)-1]
	if last.Role != RoleAssistant || len(last.ToolCalls) != 0 {
		t.Fatalf("assistant message keeps dropped tool calls: %+v", last)
	}
}

func TestHookEndFromTool(t *testing.T) {
	a := newAgent(t, Config{
		Model: Mock(TurnTool("echo", map[string]any{"text": "x"}), Turn{Text: "should not run"}),
		Tools: []Tool{echoTool("echo", nil)},
		Hooks: Hooks{AfterTool: func(ctx context.Context, e *ToolEvent) { e.End = "tool said so" }},
	})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopHook || len(res.Steps) != 1 {
		t.Fatalf("stop=%q steps=%d", res.Stop, len(res.Steps))
	}
}

func TestHookSwapRunsTheOtherTool(t *testing.T) {
	var a1, b1 atomic.Int32
	a := newAgent(t, Config{
		Model: Mock(TurnTool("old", map[string]any{"text": "hi"}), Turn{Text: "done"}),
		Tools: []Tool{echoTool("old", &a1), echoTool("new", &b1)},
		Hooks: Hooks{BeforeTool: func(ctx context.Context, e *ToolEvent) error {
			if e.Call.Name == "old" {
				e.Swap = "new"
			}
			return nil
		}},
	})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	got := res.Steps[0].ToolResults[0]
	if a1.Load() != 0 || b1.Load() != 1 || got.Content != "new:hi" || got.Name != "new" {
		t.Fatalf("old=%d new=%d result=%+v", a1.Load(), b1.Load(), got)
	}
}

func TestHookSwapStillNeedsApproval(t *testing.T) {
	var ran atomic.Int32
	var asked []string
	a := newAgent(t, Config{
		Model: Mock(TurnTool("safe", map[string]any{"text": "hi"}), Turn{Text: "done"}),
		Tools: []Tool{echoTool("safe", nil), Risky(echoTool("danger", &ran))},
		Approve: func(ctx context.Context, req Approval) (Decision, error) {
			asked = append(asked, req.Tool)
			return Reject("no"), nil
		},
		Hooks: Hooks{BeforeTool: func(ctx context.Context, e *ToolEvent) error {
			e.Swap = "danger"
			return nil
		}},
	})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 0 || len(asked) != 1 || asked[0] != "danger" || !res.Steps[0].ToolResults[0].IsError {
		t.Fatalf("ran=%d asked=%v result=%+v", ran.Load(), asked, res.Steps[0].ToolResults[0])
	}
}

func TestHookSwapCannotSwapTwice(t *testing.T) {
	a := newAgent(t, Config{
		Model: Mock(TurnTool("a", map[string]any{"text": "hi"}), Turn{Text: "done"}),
		Tools: []Tool{echoTool("a", nil), echoTool("b", nil), echoTool("c", nil)},
		Hooks: Hooks{BeforeTool: func(ctx context.Context, e *ToolEvent) error {
			switch e.Call.Name {
			case "a":
				e.Swap = "b"
			case "b":
				e.Swap = "c"
			}
			return nil
		}},
	})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Steps[0].ToolResults[0]; !r.IsError {
		t.Fatalf("double swap ran: %+v", r)
	}
}

func TestInterruptEventIsEmitted(t *testing.T) {
	a := newAgent(t, Config{
		Model:   Mock(TurnTool("danger", map[string]any{"text": "x"})),
		Tools:   []Tool{Risky(echoTool("danger", nil))},
		Approve: func(ctx context.Context, req Approval) (Decision, error) { return Pause(), nil },
	})
	var got []Interrupt
	_, err := a.Run(context.Background(), "x", Thread("t"), OnEvent(func(e Event) {
		if e.Type == EventInterrupt {
			got = e.Interrupts
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Tool != "danger" {
		t.Fatalf("interrupt event = %+v", got)
	}
}
