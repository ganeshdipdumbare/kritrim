package evals

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestJudgeCriterion(t *testing.T) {
	judge := kritrim.Mock(kritrim.Turn{Text: `{"score":0.8,"reason":"grounded"}`})
	e := Judge(Faithful, judge)
	if e.Name() != "faithful" {
		t.Fatalf("name = %q", e.Name())
	}
	s, err := e.Evaluate(context.Background(), Result{Case: Case{Input: "q"}, Output: "a"})
	if err != nil || !s.Pass || s.Value != 0.8 || s.Evaluator != "faithful" {
		t.Fatalf("%+v %v", s, err)
	}
	// Every criterion carries a rubric and a distinct label.
	names := map[string]bool{}
	for _, c := range []Criterion{Faithful, Correct, Relevant, Coherent, Concise, Helpful,
		Harmless, Unbiased, Answered, Obedient, Achieved, Recovered} {
		if len(c) < 40 {
			t.Errorf("criterion %q has no rubric", criterionName(c))
		}
		n := criterionName(c)
		if n == "judge" || names[n] {
			t.Errorf("criterion name %q is missing or duplicated", n)
		}
		names[n] = true
	}
}

// TestJudgeSeesTheTrajectory: a rubric about grounding or completion needs
// what the agent did, not only what it said.
func TestJudgeSeesTheTrajectory(t *testing.T) {
	var prompt string
	judge := &capturingModel{reply: `{"score":1,"reason":"ok"}`, prompt: &prompt}
	run := &kritrim.Result{
		Stop: "max_steps",
		Steps: []kritrim.Step{{
			ToolCalls:   []kritrim.ToolCall{{Name: "lookup", Args: map[string]any{"id": 7}}},
			ToolResults: []kritrim.ToolResult{{Name: "lookup", Content: "boom", IsError: true}},
		}},
	}
	if _, err := Judge(Achieved, judge).Evaluate(context.Background(),
		Result{Case: Case{Input: "q"}, Output: "all done", Run: run}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"called lookup", `"id":7`, "lookup FAILED", "run ended early: max_steps"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("judge prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestToolParameters(t *testing.T) {
	check := func(args map[string]any) error {
		if args["order_id"] == "" || args["order_id"] == nil {
			return errors.New("no order id")
		}
		return nil
	}
	runWith := func(calls ...kritrim.ToolCall) Result {
		return Result{Run: &kritrim.Result{Steps: []kritrim.Step{{ToolCalls: calls}}}}
	}
	e := ToolParameters("refund", check)

	s, err := e.Evaluate(context.Background(), runWith(kritrim.ToolCall{Name: "refund",
		Args: map[string]any{"order_id": "A-17"}}))
	if err != nil || !s.Pass {
		t.Fatalf("good args: %+v %v", s, err)
	}
	s, _ = e.Evaluate(context.Background(), runWith(kritrim.ToolCall{Name: "refund",
		Args: map[string]any{"order_id": ""}}))
	if s.Pass || !strings.Contains(s.Reason, "no order id") {
		t.Fatalf("bad args: %+v", s)
	}
	// One bad call among good ones fails.
	s, _ = e.Evaluate(context.Background(), runWith(
		kritrim.ToolCall{Name: "refund", Args: map[string]any{"order_id": "A-1"}},
		kritrim.ToolCall{Name: "refund", Args: map[string]any{}}))
	if s.Pass {
		t.Fatalf("a bad call passed: %+v", s)
	}
	// Never called is a failure, not a pass by omission.
	s, _ = e.Evaluate(context.Background(), runWith(kritrim.ToolCall{Name: "other"}))
	if s.Pass || !strings.Contains(s.Reason, "never called") {
		t.Fatalf("uncalled: %+v", s)
	}
	// A missing check is a configuration error.
	if _, err := ToolParameters("refund", nil).Evaluate(context.Background(), Result{}); err == nil {
		t.Fatal("nil check accepted")
	}
}

type capturingModel struct {
	reply  string
	prompt *string
}

func (m *capturingModel) Generate(ctx context.Context, req kritrim.Request) (*kritrim.Response, error) {
	if len(req.Messages) > 0 {
		*m.prompt = req.Messages[len(req.Messages)-1].Content
	}
	return &kritrim.Response{Text: m.reply, Stop: "stop"}, nil
}

func (m *capturingModel) Stream(ctx context.Context, req kritrim.Request, emit func(kritrim.Delta)) (*kritrim.Response, error) {
	return m.Generate(ctx, req)
}
