package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ganeshdipdumbare/kritrim"
)

// GoalConfig configures GoalLoop.
type GoalConfig struct {
	// Goal is what has to be true when the work is finished. Write it as a
	// test, not a wish: "every file under ./api compiles and go test
	// ./api/... passes", not "fix the API".
	Goal string
	// Done decides whether the goal has been reached. nil means one
	// attempt, which makes GoalLoop a plain Run.
	//
	// Check the world, not the transcript. A model that says "all tests
	// pass" has only said so; run the tests. Done is called with the
	// result of the attempt that just finished.
	Done func(ctx context.Context, res *kritrim.Result) (bool, error)
	// MaxAttempts caps the attempts. Default 5. An agent that cannot
	// finish in a handful of tries is usually stuck on something it cannot
	// fix, and more attempts spend tokens to find that out again.
	MaxAttempts int
	// Thread is the conversation every attempt shares, so the agent
	// remembers what it already tried. Required: without a thread each
	// attempt starts from nothing and repeats the same first move.
	Thread string
	// Nudge builds the prompt for a retry from the attempt that failed.
	// nil asks the agent what is left and tells it to continue.
	Nudge func(attempt int, res *kritrim.Result) string
}

// GoalLoop runs an agent until a goal is met.
//
// One Run ends when the model stops calling tools, which is the model's
// judgement that it is finished — not a fact. GoalLoop closes that gap: it
// re-prompts the same thread while Done says the goal has not been reached,
// up to MaxAttempts.
//
//	res, err := plugin.GoalLoop(ctx, agent, plugin.GoalConfig{
//		Goal:   "go build ./... succeeds",
//		Thread: "fix-build",
//		Done: func(ctx context.Context, res *kritrim.Result) (bool, error) {
//			return exec.Command("go", "build", "./...").Run() == nil, nil
//		},
//	})
//
// Every prompt it sends restates the goal, so the agent is reminded of the
// finish line it is being measured against. To put the goal in the system
// prompt as well, add plugin.Goal(goal) to Config.Hooks.
//
// A run that pauses for an approval is returned as it is: a human has to
// decide before anything else can happen, and looping would ask again.
// Resume the thread with kritrim.Decisions and call GoalLoop again.
//
// ErrGoalNotMet comes back when the attempts run out, wrapped with the
// last result still available, so a caller can report what was achieved
// rather than only that it failed.
func GoalLoop(ctx context.Context, a *kritrim.Agent, cfg GoalConfig) (*kritrim.Result, error) {
	if a == nil {
		return nil, errors.New("plugin: GoalLoop needs an agent")
	}
	if strings.TrimSpace(cfg.Goal) == "" {
		return nil, errors.New("plugin: GoalLoop needs a goal")
	}
	if cfg.Thread == "" {
		return nil, errors.New("plugin: GoalLoop needs a thread so attempts share what was tried")
	}
	attempts := cfg.MaxAttempts
	if attempts <= 0 {
		attempts = 5
	}
	prompt := cfg.Goal
	var last *kritrim.Result
	for i := 1; i <= attempts; i++ {
		res, err := a.Run(ctx, prompt, kritrim.Thread(cfg.Thread))
		if err != nil {
			return last, err
		}
		last = res
		if res.Stop == kritrim.StopInterrupt {
			// Waiting on a human: returning is the only honest move.
			return res, nil
		}
		if cfg.Done == nil {
			return res, nil
		}
		done, err := cfg.Done(ctx, res)
		if err != nil {
			return res, fmt.Errorf("plugin: goal check: %w", err)
		}
		if done {
			return res, nil
		}
		if i == attempts {
			break
		}
		prompt = nudge(cfg, i, res)
	}
	return last, fmt.Errorf("%w after %d attempts: %s", ErrGoalNotMet, attempts, cfg.Goal)
}

// ErrGoalNotMet means GoalLoop ran out of attempts.
var ErrGoalNotMet = errors.New("plugin: goal not met")

func nudge(cfg GoalConfig, attempt int, res *kritrim.Result) string {
	if cfg.Nudge != nil {
		return cfg.Nudge(attempt, res)
	}
	return fmt.Sprintf("That did not meet the goal yet:\n\n%s\n\n"+
		"Work out what is still missing and fix it. Do not repeat a step that already worked.", cfg.Goal)
}

// Goal keeps a goal in front of the model on every call. GoalLoop applies
// it for you; add it to Config.Hooks when you drive the loop yourself.
func Goal(goal string) kritrim.Hooks {
	return Inject(func(ctx context.Context) string {
		if goal == "" {
			return ""
		}
		return "Your goal for this task:\n" + goal + "\n\nDo not stop until it is met."
	})
}
