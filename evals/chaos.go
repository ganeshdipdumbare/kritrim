package evals

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// ChaosConfig configures Chaos.
type ChaosConfig struct {
	// Fail maps a tool name to how many of its first calls fail in each
	// run. The name "*" applies to every tool that has no entry of its
	// own.
	//
	//	Fail: map[string]int{"http_request": 1}
	//
	// counts per run, not per experiment, so every case meets the same
	// weather.
	Fail map[string]int
	// Message is the failure the model is told about. Default
	// "the tool is temporarily unavailable".
	Message string
	// Latency delays every call to a named tool. Use it to find a run that
	// only works because a tool was fast.
	Latency time.Duration
}

// Chaos makes tools fail on purpose, so an evaluation measures what the
// agent does when the world does not cooperate.
//
//	hooks := evals.Chaos(evals.ChaosConfig{Fail: map[string]int{"http_request": 1}})
//	a, _ := kritrim.New(kritrim.Config{Model: model, Tools: tools, Hooks: hooks})
//	report, _ := evals.Experiment{
//		Cases:      cases,
//		Evaluators: []evals.Evaluator{evals.NoError(), evals.Judge(evals.Recovered, judge)},
//	}.Run(ctx, run)
//
// A suite run on a good day only proves the agent works on a good day.
// The interesting failures are the ones where a tool returns an error and
// the agent reports success anyway, retries the identical call until it
// hits the step limit, or abandons a task it could have finished another
// way.
//
// The failures are counted, not random: the same case fails the same call
// every time, so a red result is reproducible and a fix is provable. There
// is no seed to record and no flaky suite to re-run.
//
// Counting is per run, keyed on the run id, and a run with no id (no
// kritrim.RunID and no thread) shares the default bucket with other such
// runs. Give concurrent runs ids, or run them one at a time.
func Chaos(cfg ChaosConfig) kritrim.Hooks {
	msg := cfg.Message
	if msg == "" {
		msg = "the tool is temporarily unavailable"
	}
	c := &chaos{cfg: cfg, msg: msg, seen: map[string]int{}}
	return kritrim.Hooks{
		BeforeTool: func(ctx context.Context, e *kritrim.ToolEvent) error {
			if e.Call == nil {
				return nil
			}
			if cfg.Latency > 0 {
				select {
				case <-time.After(cfg.Latency):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if c.shouldFail(kritrim.RunIDFrom(ctx), e.Call.Name) {
				e.Deny = fmt.Sprintf("%s (injected by evals.Chaos)", c.msg)
			}
			return nil
		},
	}
}

type chaos struct {
	cfg ChaosConfig
	msg string

	mu   sync.Mutex
	seen map[string]int // run id + tool -> calls so far
}

func (c *chaos) shouldFail(run, tool string) bool {
	budget, ok := c.cfg.Fail[tool]
	if !ok {
		budget = c.cfg.Fail["*"]
	}
	if budget <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := run + "\x00" + tool
	c.seen[key]++
	return c.seen[key] <= budget
}

// FailedTools counts the tool results that came back as errors. Use it to
// confirm Chaos actually bit before reading a recovery score: a perfect
// score on a run where nothing failed measures nothing.
func FailedTools(r Result) int {
	if r.Run == nil {
		return 0
	}
	n := 0
	for _, step := range r.Run.Steps {
		for _, res := range step.ToolResults {
			if res.IsError {
				n++
			}
		}
	}
	return n
}

// Resilient passes when the run met a tool failure and still finished with
// an answer. It is the cheap, deterministic half of recovery testing: it
// proves the agent did not die, where evals.Judge(evals.Recovered, model)
// asks whether what it did instead made sense.
//
// A run where nothing failed passes with a reason saying so, so this
// evaluator never turns green into red for the wrong reason. Check
// FailedTools when you need to be sure the failure happened.
func Resilient() Evaluator { return resilientEval{} }

type resilientEval struct{}

func (resilientEval) Name() string { return "resilient" }

func (resilientEval) Evaluate(ctx context.Context, r Result) (Score, error) {
	if r.Err != "" {
		return Score{Evaluator: "resilient", Reason: "the run returned an error: " + r.Err}, nil
	}
	failed := FailedTools(r)
	if failed == 0 {
		return Score{Evaluator: "resilient", Value: 1, Pass: true, Reason: "no tool failed"}, nil
	}
	if r.Run != nil && r.Run.Stop != "stop" {
		return Score{Evaluator: "resilient",
			Reason: fmt.Sprintf("%d tool failure(s) and the run ended as %q", failed, r.Run.Stop)}, nil
	}
	if r.Output == "" {
		return Score{Evaluator: "resilient",
			Reason: fmt.Sprintf("%d tool failure(s) and no answer", failed)}, nil
	}
	return Score{Evaluator: "resilient", Value: 1, Pass: true,
		Reason: fmt.Sprintf("answered despite %d tool failure(s)", failed)}, nil
}
