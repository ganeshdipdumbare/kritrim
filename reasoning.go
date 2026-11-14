package kritrim

import "fmt"

// Reasoning asks the model to think before it answers.
//
// Effort is the portable setting: every provider that supports reasoning
// maps "low", "medium" and "high" onto its own control (Anthropic extended
// thinking, OpenAI reasoning effort, Gemini thinking budget, Bedrock
// reasoning config). Budget overrides the token allowance Effort implies
// when you need an exact number.
//
//	a, _ := kritrim.New(kritrim.Config{
//		Model:     kritrim.Anthropic("claude-sonnet-5-5"),
//		Reasoning: &kritrim.Reasoning{Effort: "high"},
//	})
//
// Reasoning tokens are output tokens: they count against MaxOutputTokens
// and against the run's token budget.
type Reasoning struct {
	// Effort is "low", "medium" or "high". Empty with a Budget set is
	// derived from the budget.
	Effort string
	// Budget is the thinking-token allowance. Zero uses the budget for
	// Effort (low 1024, medium 4096, high 16384).
	Budget int
	// Include asks providers that can return their reasoning to stream it
	// as "reasoning" deltas. Providers that only return a summary return
	// the summary.
	Include bool
}

// Effort budgets. Anthropic's minimum thinking budget is 1024 tokens, so
// "low" starts there.
const (
	budgetLow    = 1024
	budgetMedium = 4096
	budgetHigh   = 16384
)

func (r *Reasoning) validate() error {
	if r == nil {
		return nil
	}
	switch r.Effort {
	case "", "low", "medium", "high":
	default:
		return fmt.Errorf("kritrim: Reasoning.Effort %q must be low, medium or high", r.Effort)
	}
	if r.Effort == "" && r.Budget <= 0 {
		return fmt.Errorf("kritrim: Reasoning needs an Effort or a Budget")
	}
	if r.Budget < 0 {
		return fmt.Errorf("kritrim: Reasoning.Budget cannot be negative")
	}
	if r.Budget > 0 && r.Budget < budgetLow {
		return fmt.Errorf("kritrim: Reasoning.Budget %d is below the %d-token minimum", r.Budget, budgetLow)
	}
	return nil
}

// budget is the thinking-token allowance for providers that take a number.
func (r *Reasoning) budget() int {
	if r == nil {
		return 0
	}
	if r.Budget > 0 {
		return r.Budget
	}
	switch r.Effort {
	case "low":
		return budgetLow
	case "high":
		return budgetHigh
	default:
		return budgetMedium
	}
}

// effort is the named level for providers that take a word.
func (r *Reasoning) effort() string {
	if r == nil {
		return ""
	}
	if r.Effort != "" {
		return r.Effort
	}
	switch {
	case r.Budget <= 2048:
		return "low"
	case r.Budget <= 8192:
		return "medium"
	default:
		return "high"
	}
}
