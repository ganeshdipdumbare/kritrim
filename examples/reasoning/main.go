// Reasoning, routing and a context budget: the three knobs that decide how
// much a run thinks, which model does the thinking, and how much history
// it thinks about. No API key, no network.
//
//	go run ./examples/reasoning
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/ganeshdipdumbare/kritrim"
)

func main() {
	// Two models standing in for a cheap one and an expensive one. Mock
	// names itself, so the example can show which one a run picked.
	small := named{"small", kritrim.Mock(kritrim.Turn{Text: "Paris."})}
	large := named{"large", kritrim.Mock(kritrim.Turn{Text: "It depends on three things: ..."})}
	// The summarizer is its own model: a cheap one, pinned, so a change in
	// the summary means the history changed and not the model.
	summarizer := named{"summarizer", kritrim.Mock(kritrim.Turn{
		Text: "The user asked for the capital of France and was told Paris."})}

	a, err := kritrim.New(kritrim.Config{
		Model: small,

		// Route decides up front, from the prompt. Fallback is the other
		// half of the story: it reacts to a failure. Use both — route for
		// cost, fall back for reliability.
		Route: func(ctx context.Context, prompt string) kritrim.Model {
			if len(prompt) > 60 || strings.Contains(strings.ToLower(prompt), "why") {
				fmt.Println("  route: large")
				return large
			}
			fmt.Println("  route: small (Config.Model)")
			return nil // nil means Config.Model
		},

		// Reasoning is portable: "high" becomes extended thinking on
		// Anthropic, a reasoning effort on OpenAI, a thinking budget on
		// Gemini. Thinking tokens are output tokens, so they count
		// against MaxOutputTokens and the run's budget.
		Reasoning: &kritrim.Reasoning{Effort: "high"},

		// Nothing is trimmed while the history fits in MaxTokens. Over
		// that, the strategy decides. KeepWindow is the default and makes
		// no model call; SummarizeWith(model, "") pays for one and keeps
		// the gist.
		Context: &kritrim.ContextConfig{
			MaxTokens: 30, // tiny, so the second call has to compact
			KeepLast:  2,
			Strategy:  kritrim.SummarizeWith(summarizer, ""),
		},

		// Printing what each call actually sends is the only way to see
		// compaction happen.
		Hooks: kritrim.Hooks{
			BeforeModel: func(ctx context.Context, e *kritrim.ModelEvent) error {
				fmt.Printf("  sending %d message(s), about %d tokens",
					len(e.Request.Messages), kritrim.EstimateTokens(e.Request.Messages))
				if len(e.Request.Messages) > 0 {
					fmt.Printf(" starting %q", truncate(e.Request.Messages[0].Content, 50))
				}
				fmt.Println()
				return nil
			},
		},

		Session: kritrim.MemorySessions(),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer a.Close()

	ctx := context.Background()
	for _, prompt := range []string{
		"Capital of France?",
		"Why is the sky blue, and what would make it another colour on another planet?",
	} {
		fmt.Printf("\n> %s\n", prompt)
		res, err := a.Run(ctx, prompt, kritrim.Thread("demo"))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %s\n", res.Text)
	}

	// EstimateTokens is the number kritrim budgets with, and it is exported
	// so you can check a history yourself before sending it anywhere.
	sess, err := a.Session(ctx, "demo")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\nthread: %d messages, about %d tokens\n",
		len(sess.Messages), kritrim.EstimateTokens(sess.Messages))
	fmt.Println("The session keeps every message; compaction only changes what a call sends.")
}

// named gives a model a name so Route's choice is visible in the output and
// in the audit log.
type named struct {
	name string
	kritrim.Model
}

func (n named) Name() string { return n.name }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
