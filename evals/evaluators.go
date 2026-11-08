package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// Exact passes when the trimmed output equals Expected.
func Exact() Evaluator { return exactEval{} }

type exactEval struct{}

func (exactEval) Name() string { return "exact" }
func (exactEval) Evaluate(ctx context.Context, r Result) (Score, error) {
	ok := strings.TrimSpace(r.Output) == strings.TrimSpace(r.Case.Expected)
	return Score{Evaluator: "exact", Value: b2f(ok), Pass: ok,
		Reason: matchReason(ok, r.Case.Expected, r.Output)}, nil
}

// Contains passes when the output contains Expected, ignoring case.
func Contains() Evaluator { return containsEval{} }

type containsEval struct{}

func (containsEval) Name() string { return "contains" }
func (containsEval) Evaluate(ctx context.Context, r Result) (Score, error) {
	ok := strings.Contains(strings.ToLower(r.Output), strings.ToLower(r.Case.Expected))
	return Score{Evaluator: "contains", Value: b2f(ok), Pass: ok}, nil
}

// Regex passes when the pattern matches the output.
func Regex(pattern string) (Evaluator, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("evals: regex: %w", err)
	}
	return regexEval{re: re}, nil
}

type regexEval struct{ re *regexp.Regexp }

func (regexEval) Name() string { return "regex" }
func (e regexEval) Evaluate(ctx context.Context, r Result) (Score, error) {
	ok := e.re.MatchString(r.Output)
	return Score{Evaluator: "regex", Value: b2f(ok), Pass: ok}, nil
}

// JSONFields passes when the output parses as JSON and every expected field
// matches.
func JSONFields() Evaluator { return jsonEval{} }

type jsonEval struct{}

func (jsonEval) Name() string { return "json" }
func (jsonEval) Evaluate(ctx context.Context, r Result) (Score, error) {
	if len(r.Case.Expect) == 0 {
		return Score{Evaluator: "json", Pass: true, Value: 1}, nil
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stripFence(r.Output)), &got); err != nil {
		return Score{Evaluator: "json", Value: 0, Pass: false, Reason: err.Error()}, nil
	}
	for k, want := range r.Case.Expect {
		if fmt.Sprint(got[k]) != fmt.Sprint(want) {
			return Score{Evaluator: "json", Value: 0, Pass: false,
				Reason: fmt.Sprintf("%s: want %v, got %v", k, want, got[k])}, nil
		}
	}
	return Score{Evaluator: "json", Value: 1, Pass: true}, nil
}

// ToolCallConfig constrains the tool calls a run made.
type ToolCallConfig struct {
	Require []string
	Forbid  []string
	Exact   []string
}

// ToolCalls scores the run's tool trajectory.
func ToolCalls(cfg ToolCallConfig) Evaluator { return toolEval{cfg: cfg} }

type toolEval struct{ cfg ToolCallConfig }

func (toolEval) Name() string { return "tool_calls" }
func (e toolEval) Evaluate(ctx context.Context, r Result) (Score, error) {
	if r.Run == nil {
		return Score{Evaluator: "tool_calls", Pass: false, Reason: "no run"}, nil
	}
	var names []string
	for _, c := range r.Run.ToolCalls() {
		names = append(names, c.Name)
	}
	has := func(name string) bool {
		for _, n := range names {
			if n == name {
				return true
			}
		}
		return false
	}
	for _, req := range e.cfg.Require {
		if !has(req) {
			return Score{Evaluator: "tool_calls", Pass: false, Value: 0, Reason: "missing " + req}, nil
		}
	}
	for _, bad := range e.cfg.Forbid {
		if has(bad) {
			return Score{Evaluator: "tool_calls", Pass: false, Value: 0, Reason: "forbidden " + bad}, nil
		}
	}
	if len(e.cfg.Exact) > 0 {
		if len(names) != len(e.cfg.Exact) {
			return Score{Evaluator: "tool_calls", Pass: false, Value: 0,
				Reason: fmt.Sprintf("want %v, got %v", e.cfg.Exact, names)}, nil
		}
		for i := range names {
			if names[i] != e.cfg.Exact[i] {
				return Score{Evaluator: "tool_calls", Pass: false, Value: 0,
					Reason: fmt.Sprintf("want %v, got %v", e.cfg.Exact, names)}, nil
			}
		}
	}
	return Score{Evaluator: "tool_calls", Pass: true, Value: 1}, nil
}

// NoError passes when the run returned no error.
func NoError() Evaluator { return noErrorEval{} }

type noErrorEval struct{}

func (noErrorEval) Name() string { return "no_error" }
func (noErrorEval) Evaluate(ctx context.Context, r Result) (Score, error) {
	ok := r.Err == ""
	return Score{Evaluator: "no_error", Value: b2f(ok), Pass: ok, Reason: r.Err}, nil
}

// MaxSteps passes when the run stopped before the step budget.
func MaxSteps() Evaluator { return stepEval{} }

type stepEval struct{}

func (stepEval) Name() string { return "max_steps" }
func (stepEval) Evaluate(ctx context.Context, r Result) (Score, error) {
	ok := r.Run != nil && r.Run.Stop != "max_steps"
	return Score{Evaluator: "max_steps", Value: b2f(ok), Pass: ok, Reason: "stop=" + stopOf(r)}, nil
}

// MaxTokens passes when the run stayed within n total tokens.
func MaxTokens(n int) Evaluator { return tokenEval{n: n} }

type tokenEval struct{ n int }

func (tokenEval) Name() string { return "max_tokens" }
func (e tokenEval) Evaluate(ctx context.Context, r Result) (Score, error) {
	ok := r.Tokens <= e.n
	return Score{Evaluator: "max_tokens", Value: b2f(ok), Pass: ok,
		Reason: fmt.Sprintf("%d tokens, limit %d", r.Tokens, e.n)}, nil
}

// MaxDuration passes when the run took less than d.
func MaxDuration(d time.Duration) Evaluator { return durationEval{d: d} }

type durationEval struct{ d time.Duration }

func (durationEval) Name() string { return "max_duration" }
func (e durationEval) Evaluate(ctx context.Context, r Result) (Score, error) {
	ok := r.Duration <= e.d
	return Score{Evaluator: "max_duration", Value: b2f(ok), Pass: ok,
		Reason: r.Duration.String()}, nil
}

// JudgeConfig configures an LLM judge.
type JudgeConfig struct {
	Model     kritrim.Model
	Rubric    string
	Threshold float64
	// System overrides the judge system prompt.
	System string
}

// LLMJudge scores the output with a model against a rubric.
func LLMJudge(cfg JudgeConfig) Evaluator { return judgeEval{cfg: cfg} }

type judgeEval struct{ cfg JudgeConfig }

func (judgeEval) Name() string { return "llm_judge" }

func (e judgeEval) Evaluate(ctx context.Context, r Result) (Score, error) {
	if e.cfg.Model == nil {
		return Score{}, fmt.Errorf("evals: judge needs a model")
	}
	threshold := e.cfg.Threshold
	if threshold <= 0 {
		threshold = 0.5
	}
	system := e.cfg.System
	if system == "" {
		system = "You grade an AI assistant's answer against a rubric. Be strict and concise."
	}
	prompt := fmt.Sprintf("Rubric:\n%s\n\nUser input:\n%s\n\nExpected (may be empty):\n%s\n\nAnswer:\n%s",
		e.cfg.Rubric, r.Case.Input, r.Case.Expected, r.Output)
	schema := kritrim.SchemaOf(judgment{})
	resp, err := e.cfg.Model.Generate(ctx, kritrim.Request{
		System:     system,
		Messages:   []kritrim.Message{{Role: kritrim.RoleUser, Content: prompt}},
		Schema:     schema,
		SchemaName: "verdict",
		MaxTokens:  500,
	})
	if err != nil {
		return Score{}, err
	}
	var v judgment
	if err := json.Unmarshal([]byte(stripFence(resp.Text)), &v); err != nil {
		return Score{Evaluator: "llm_judge", Pass: false, Reason: "unparseable judge reply"}, nil
	}
	if v.Score < 0 {
		v.Score = 0
	}
	if v.Score > 1 {
		v.Score = 1
	}
	return Score{Evaluator: "llm_judge", Value: v.Score, Pass: v.Score >= threshold, Reason: v.Reason}, nil
}

type judgment struct {
	Score  float64 `json:"score" desc:"0.0 to 1.0"`
	Reason string  `json:"reason" desc:"one sentence"`
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func matchReason(ok bool, want, got string) string {
	if ok {
		return ""
	}
	return fmt.Sprintf("want %q, got %q", want, got)
}

func stopOf(r Result) string {
	if r.Run == nil {
		return ""
	}
	return r.Run.Stop
}

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		s = strings.TrimPrefix(s, "json")
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	return strings.TrimSpace(s)
}
