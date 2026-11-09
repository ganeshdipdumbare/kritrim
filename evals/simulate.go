package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ganeshdipdumbare/kritrim"
)

// GenerateCases asks a model for test cases from a description of the agent.
func GenerateCases(ctx context.Context, model kritrim.Model, contextText string, n int) ([]Case, error) {
	if model == nil {
		return nil, fmt.Errorf("evals: GenerateCases needs a model")
	}
	if n <= 0 {
		n = 5
	}
	schema := kritrim.SchemaOf(struct {
		Cases []Case `json:"cases"`
	}{})
	prompt := fmt.Sprintf("Generate %d test cases for this agent. Return JSON only.\n\nAgent context:\n%s", n, contextText)
	resp, err := model.Generate(ctx, kritrim.Request{
		System:   "You write concise, varied evaluation cases. Each case needs a name and input; expected is optional.",
		Messages: []kritrim.Message{{Role: kritrim.RoleUser, Content: prompt}},
		Schema:   schema, SchemaName: "cases", MaxTokens: 2000,
	})
	if err != nil {
		return nil, err
	}
	var out struct {
		Cases []Case `json:"cases"`
	}
	if err := json.Unmarshal([]byte(stripFence(resp.Text)), &out); err != nil {
		return nil, fmt.Errorf("evals: generate: %w", err)
	}
	if len(out.Cases) > n {
		out.Cases = out.Cases[:n]
	}
	return out.Cases, nil
}

// SimConfig configures a simulated user.
type SimConfig struct {
	// Model plays the user. A different model from the agent under test is
	// best, to avoid echo.
	Model    kritrim.Model
	Persona  string
	Goal     string
	MaxTurns int
	Judge    Evaluator
}

// Simulate runs a multi-turn conversation with a model playing the user and
// optionally scores the outcome.
func Simulate(ctx context.Context, cfg SimConfig, run func(ctx context.Context, prompt string) (*kritrim.Result, error)) (*Result, error) {
	if cfg.Model == nil {
		return nil, fmt.Errorf("evals: Simulate needs a user model")
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 6
	}
	var transcript strings.Builder
	var last *kritrim.Result
	r := Result{Case: Case{Name: "simulation", Input: cfg.Goal}}
	for turn := 0; turn < cfg.MaxTurns; turn++ {
		next, done, err := nextUser(ctx, cfg, transcript.String())
		if err != nil {
			r.Err = err.Error()
			break
		}
		if done {
			break
		}
		res, err := run(ctx, next)
		if err != nil {
			r.Err = err.Error()
			break
		}
		last = res
		fmt.Fprintf(&transcript, "user: %s\nassistant: %s\n", next, res.Text)
		if res.Stop != "stop" {
			break
		}
	}
	if last != nil {
		r.Run = last
		r.Output = last.Text
		r.Tokens = last.Usage.Total()
	}
	if cfg.Judge != nil {
		sc, err := cfg.Judge.Evaluate(ctx, r)
		if err != nil {
			sc = Score{Evaluator: cfg.Judge.Name(), Pass: false, Reason: err.Error()}
		}
		r.Scores = []Score{sc}
	}
	r.Pass = r.Err == "" && allPass(r.Scores)
	if !r.Pass {
		r.Findings = Detect(r)
	}
	return &r, nil
}

func nextUser(ctx context.Context, cfg SimConfig, transcript string) (string, bool, error) {
	prompt := fmt.Sprintf("Persona: %s\nGoal: %s\n\nConversation so far:\n%s\n\n"+
		"Reply with the next user message only, or the word DONE if the goal is met or the conversation is over.",
		cfg.Persona, cfg.Goal, transcript)
	resp, err := cfg.Model.Generate(ctx, kritrim.Request{
		System:    "You simulate a user testing an AI agent. Stay in character and keep each message short.",
		Messages:  []kritrim.Message{{Role: kritrim.RoleUser, Content: prompt}},
		MaxTokens: 200,
	})
	if err != nil {
		return "", false, err
	}
	next := strings.TrimSpace(resp.Text)
	if strings.EqualFold(next, "DONE") || next == "" {
		return "", true, nil
	}
	return next, false, nil
}
