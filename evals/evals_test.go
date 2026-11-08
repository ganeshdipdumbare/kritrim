package evals

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestEvaluators(t *testing.T) {
	ctx := context.Background()
	run := &kritrim.Result{Text: "Paris", Stop: "stop", Steps: []kritrim.Step{{
		ToolCalls: []kritrim.ToolCall{{Name: "lookup"}},
	}}}
	r := Result{Case: Case{Name: "c", Input: "capital", Expected: "Paris", Expect: map[string]any{"x": "1"}},
		Output: `{"x":"1"}`, Run: run}
	r.Output = "Paris"
	for _, ev := range []Evaluator{Exact(), Contains(), NoError(), MaxSteps(), ToolCalls(ToolCallConfig{Require: []string{"lookup"}})} {
		s, err := ev.Evaluate(ctx, r)
		if err != nil || !s.Pass {
			t.Fatalf("%s: %+v %v", ev.Name(), s, err)
		}
	}
	re, err := Regex(`^Par`)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := re.Evaluate(ctx, r); !s.Pass {
		t.Fatal("regex failed")
	}
	j := Result{Output: `{"x":"1"}`, Case: Case{Expect: map[string]any{"x": "1"}}}
	if s, _ := JSONFields().Evaluate(ctx, j); !s.Pass {
		t.Fatalf("json failed: %+v", s)
	}
	if s, _ := ToolCalls(ToolCallConfig{Forbid: []string{"lookup"}}).Evaluate(ctx, r); s.Pass {
		t.Fatal("forbidden tool passed")
	}
}

func TestExperiment(t *testing.T) {
	cases := []Case{{Name: "a", Input: "x", Expected: "hi"}, {Name: "b", Input: "y", Expected: "hi"}}
	exp := Experiment{Cases: cases, Evaluators: []Evaluator{Exact(), NoError()}, Concurrency: 2, Detect: true,
		Cache: DirCache(t.TempDir()), CacheKey: "v1"}
	calls := &atomic.Int32{}
	report, err := exp.Run(context.Background(), func(ctx context.Context, c Case) (*kritrim.Result, error) {
		calls.Add(1)
		return &kritrim.Result{Text: "hi", Stop: "stop"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed != 2 || !report.OK() {
		t.Fatalf("%+v", report)
	}
	if err := report.WriteJSON(&strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	// Second run should hit the cache.
	if _, err := exp.Run(context.Background(), func(ctx context.Context, c Case) (*kritrim.Result, error) {
		t.Fatal("cache miss")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls %d", calls.Load())
	}
}

func TestDetect(t *testing.T) {
	r := Result{Output: "error", Run: &kritrim.Result{Stop: "max_steps", Steps: []kritrim.Step{{
		ToolCalls:   []kritrim.ToolCall{{Name: "x", Args: map[string]any{"a": 1}}, {Name: "x", Args: map[string]any{"a": 1}}},
		ToolResults: []kritrim.ToolResult{{Name: "x", Content: "boom", IsError: true}},
	}}}}
	f := Detect(r)
	kinds := map[string]bool{}
	for _, x := range f {
		kinds[x.Kind] = true
	}
	for _, want := range []string{"max_steps", "tool_error", "loop"} {
		if !kinds[want] {
			t.Fatalf("missing %s in %+v", want, f)
		}
	}
}

func TestJudge(t *testing.T) {
	judge := kritrim.Mock(kritrim.Turn{Text: `{"score":0.9,"reason":"good"}`})
	s, err := LLMJudge(JudgeConfig{Model: judge, Rubric: "be right"}).Evaluate(context.Background(),
		Result{Case: Case{Input: "q"}, Output: "a"})
	if err != nil || !s.Pass || s.Value != 0.9 {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestRunErrorFails(t *testing.T) {
	exp := Experiment{Cases: []Case{{Name: "a"}}, Evaluators: []Evaluator{NoError()}}
	rep, err := exp.Run(context.Background(), func(ctx context.Context, c Case) (*kritrim.Result, error) {
		return nil, errors.New("boom")
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed != 1 || rep.Results[0].Pass {
		t.Fatalf("%+v", rep.Results[0])
	}
}
