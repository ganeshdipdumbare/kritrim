package evals

import (
	"context"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

// TestChaosFailsTheFirstCallThenLetsItThrough is the whole contract:
// counted failures, not random ones.
func TestChaosFailsTheFirstCallThenLetsItThrough(t *testing.T) {
	calls := 0
	lookup := kritrim.NewTool("lookup", "look something up", func(ctx context.Context, in struct {
		ID string `json:"id"`
	}) (string, error) {
		calls++
		return "found " + in.ID, nil
	})
	a, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(
			kritrim.TurnTool("lookup", map[string]any{"id": "7"}),
			kritrim.TurnTool("lookup", map[string]any{"id": "7"}),
			kritrim.Turn{Text: "found it on the second try"},
		),
		Tools: []kritrim.Tool{lookup},
		Hooks: Chaos(ChaosConfig{Fail: map[string]int{"lookup": 1}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	res, err := a.Run(context.Background(), "look up 7", kritrim.RunID("r1"))
	if err != nil {
		t.Fatal(err)
	}
	first := res.Steps[0].ToolResults[0]
	if !first.IsError || !strings.Contains(first.Content, "temporarily unavailable") {
		t.Fatalf("first result = %+v", first)
	}
	if second := res.Steps[1].ToolResults[0]; second.IsError {
		t.Fatalf("second result = %+v", second)
	}
	// The real tool ran once: the injected failure never reached it.
	if calls != 1 {
		t.Fatalf("tool ran %d times, want 1", calls)
	}
	if got := FailedTools(Result{Run: res}); got != 1 {
		t.Fatalf("FailedTools = %d", got)
	}
}

// TestChaosCountsPerRun: every case meets the same weather, so a suite
// stays comparable.
func TestChaosCountsPerRun(t *testing.T) {
	c := Chaos(ChaosConfig{Fail: map[string]int{"*": 1}})
	fails := 0
	for _, run := range []string{"r1", "r1", "r2", "r2"} {
		ctx := runContext(t, run)
		e := &kritrim.ToolEvent{Call: &kritrim.ToolCall{Name: "lookup"}}
		if err := c.BeforeTool(ctx, e); err != nil {
			t.Fatal(err)
		}
		if e.Deny != "" {
			fails++
		}
	}
	// One failure for r1, one for r2.
	if fails != 2 {
		t.Fatalf("%d failures, want 2", fails)
	}
}

func TestChaosOnlyNamedTools(t *testing.T) {
	c := Chaos(ChaosConfig{Fail: map[string]int{"lookup": 1}})
	ctx := runContext(t, "r1")
	safe := &kritrim.ToolEvent{Call: &kritrim.ToolCall{Name: "current_time"}}
	if err := c.BeforeTool(ctx, safe); err != nil || safe.Deny != "" {
		t.Fatalf("unnamed tool failed: %q %v", safe.Deny, err)
	}
}

func TestResilient(t *testing.T) {
	cases := []struct {
		name string
		r    Result
		pass bool
		why  string
	}{
		{"nothing failed", Result{Output: "hi", Run: &kritrim.Result{Stop: "stop"}}, true, "no tool failed"},
		{"recovered", Result{Output: "hi", Run: &kritrim.Result{Stop: "stop", Steps: []kritrim.Step{
			{ToolResults: []kritrim.ToolResult{{IsError: true}}}}}}, true, "despite 1"},
		{"ran out of steps", Result{Output: "hi", Run: &kritrim.Result{Stop: "max_steps", Steps: []kritrim.Step{
			{ToolResults: []kritrim.ToolResult{{IsError: true}}}}}}, false, "max_steps"},
		{"no answer", Result{Run: &kritrim.Result{Stop: "stop", Steps: []kritrim.Step{
			{ToolResults: []kritrim.ToolResult{{IsError: true}}}}}}, false, "no answer"},
		{"run errored", Result{Err: "boom"}, false, "returned an error"},
	}
	for _, tc := range cases {
		s, err := Resilient().Evaluate(context.Background(), tc.r)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if s.Pass != tc.pass || !strings.Contains(s.Reason, tc.why) {
			t.Errorf("%s: %+v", tc.name, s)
		}
	}
}

// runContext builds the run context a hook reads its run id from, by
// running one tool call through a real agent.
func runContext(t *testing.T, runID string) context.Context {
	t.Helper()
	var got context.Context
	a, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(kritrim.TurnTool("current_time", map[string]any{}), kritrim.Turn{Text: "ok"}),
		Tools: []kritrim.Tool{kritrim.Time()},
		Hooks: kritrim.Hooks{BeforeTool: func(ctx context.Context, e *kritrim.ToolEvent) error {
			got = ctx
			return nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Run(context.Background(), "time?", kritrim.RunID(runID)); err != nil {
		t.Fatal(err)
	}
	return got
}
