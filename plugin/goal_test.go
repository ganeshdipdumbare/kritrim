package plugin_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/plugin"
)

// TestGoalLoopRetriesUntilDone: the first attempt does not meet the goal,
// the second does, and both attempts share one thread.
func TestGoalLoopRetriesUntilDone(t *testing.T) {
	a, err := kritrim.New(kritrim.Config{
		Model:   kritrim.Mock(kritrim.Turn{Text: "half done"}, kritrim.Turn{Text: "all done"}),
		Session: kritrim.MemorySessions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	checks := 0
	res, err := plugin.GoalLoop(context.Background(), a, plugin.GoalConfig{
		Goal:   "everything is done",
		Thread: "t1",
		Done: func(ctx context.Context, res *kritrim.Result) (bool, error) {
			checks++
			return strings.HasPrefix(res.Text, "all"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "all done" || checks != 2 {
		t.Fatalf("text=%q checks=%d", res.Text, checks)
	}
	// One thread: the second attempt saw the first one's history.
	sess, err := a.Session(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Messages) != 4 {
		t.Fatalf("thread has %d messages, want 4 (two prompts, two replies)", len(sess.Messages))
	}
	// The retry restated the goal.
	if !strings.Contains(sess.Messages[2].Content, "everything is done") {
		t.Fatalf("retry prompt = %q", sess.Messages[2].Content)
	}
}

func TestGoalLoopGivesUp(t *testing.T) {
	a, err := kritrim.New(kritrim.Config{
		Model:   kritrim.Mock(kritrim.Turn{Text: "nope"}, kritrim.Turn{Text: "nope"}, kritrim.Turn{Text: "nope"}),
		Session: kritrim.MemorySessions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	res, err := plugin.GoalLoop(context.Background(), a, plugin.GoalConfig{
		Goal: "impossible", Thread: "t1", MaxAttempts: 2,
		Done: func(ctx context.Context, res *kritrim.Result) (bool, error) { return false, nil },
	})
	if !errors.Is(err, plugin.ErrGoalNotMet) {
		t.Fatalf("err = %v", err)
	}
	// The last result comes back with the error, so a caller can report
	// what was achieved.
	if res == nil || res.Text != "nope" {
		t.Fatalf("res = %+v", res)
	}
}

func TestGoalLoopValidates(t *testing.T) {
	a, err := kritrim.New(kritrim.Config{Model: kritrim.Mock()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, cfg := range []plugin.GoalConfig{
		{Thread: "t"}, // no goal
		{Goal: "g"},   // no thread
	} {
		if _, err := plugin.GoalLoop(context.Background(), a, cfg); err == nil {
			t.Errorf("accepted %+v", cfg)
		}
	}
	if _, err := plugin.GoalLoop(context.Background(), nil, plugin.GoalConfig{Goal: "g", Thread: "t"}); err == nil {
		t.Error("accepted a nil agent")
	}
}

// TestGoalLoopReturnsAnInterrupt: a pause needs a human, so the loop stops
// instead of asking again.
func TestGoalLoopReturnsAnInterrupt(t *testing.T) {
	refund := kritrim.Risky(kritrim.NewTool("refund", "refund", func(ctx context.Context, in struct {
		ID string `json:"id"`
	}) (string, error) {
		return "refunded", nil
	}))
	a, err := kritrim.New(kritrim.Config{
		Model:   kritrim.Mock(kritrim.TurnTool("refund", map[string]any{"id": "A-17"})),
		Tools:   []kritrim.Tool{refund},
		Session: kritrim.MemorySessions(),
		Approve: func(ctx context.Context, req kritrim.Approval) (kritrim.Decision, error) {
			return kritrim.Pause(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	calls := 0
	res, err := plugin.GoalLoop(context.Background(), a, plugin.GoalConfig{
		Goal: "refund A-17", Thread: "t1", MaxAttempts: 3,
		Done: func(ctx context.Context, res *kritrim.Result) (bool, error) { calls++; return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != "interrupt" {
		t.Fatalf("stop = %q", res.Stop)
	}
	if calls != 0 {
		t.Fatalf("goal checked %d times on a paused run", calls)
	}
}

func TestGoalHook(t *testing.T) {
	var system string
	a, err := kritrim.New(kritrim.Config{
		Model:        kritrim.Mock(kritrim.Turn{Text: "ok"}),
		Instructions: "Be brief.",
		Hooks: kritrim.Chain(plugin.Goal("ship the thing"), kritrim.Hooks{
			BeforeModel: func(ctx context.Context, e *kritrim.ModelEvent) error {
				system = e.Request.System
				return nil
			},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(system, "Be brief.") || !strings.Contains(system, "ship the thing") {
		t.Fatalf("system = %q", system)
	}
}
