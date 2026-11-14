package kritrim

import (
	"context"
	"fmt"
	"strings"
)

// ContextConfig keeps the prompt inside a token budget. It is off by
// default: with no ContextConfig the whole history is sent every step, and
// a long thread eventually hits the provider's own limit.
//
//	Context: &kritrim.ContextConfig{
//		MaxTokens: 60_000,
//		KeepLast:  30,
//		Strategy:  kritrim.SummarizeWith(model, ""),
//	}
//
// Nothing happens while the history fits. Once EstimateTokens goes over
// MaxTokens, Strategy decides what to send; the stored session always keeps
// the full history, so compaction never loses a message on disk.
type ContextConfig struct {
	// MaxTokens is the estimated input budget. Zero means no trimming.
	MaxTokens int
	// KeepLast is how many recent messages a strategy should keep
	// untouched. Zero means 20.
	KeepLast int
	// Strategy compacts the history. nil means KeepWindow.
	Strategy Strategy
}

func (c *ContextConfig) keepLast() int {
	if c == nil || c.KeepLast <= 0 {
		return 20
	}
	return c.KeepLast
}

// Strategy decides what a run sends when the history no longer fits in
// ContextConfig.MaxTokens. Built-in strategies are KeepWindow (the
// default), Summarize and SummarizeWith; implement the interface for
// anything else.
//
// Compact is given the full history and the config it runs under, and
// returns the messages to send. It must return a valid conversation: a
// tool result cannot be the first message, and every tool result it keeps
// needs the assistant turn that asked for it.
type Strategy interface {
	Compact(ctx context.Context, msgs []Message, cfg ContextConfig) ([]Message, error)
}

// EstimateTokens is the estimate kritrim budgets with: roughly four
// characters per token, plus a small per-message overhead. It is
// deliberately provider-independent and cheap, so it never needs a network
// call; expect it to be within about 10% of a real tokenizer for English
// prose, and to under-count dense JSON.
func EstimateTokens(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)/4 + 8
		n += len(m.Reasoning) / 4
		for _, c := range m.ToolCalls {
			n += len(c.Name)/4 + len(jsonString(c.Args))/4
		}
		for _, img := range m.Images {
			// A rough per-image cost; providers differ, and an image is
			// never free.
			n += len(img.Data)/1000 + 100
		}
	}
	return n
}

// KeepWindow drops the oldest messages and keeps the last
// ContextConfig.KeepLast, cutting only where the conversation stays valid.
// It is the default strategy: cheap, lossy, and it never makes a model
// call.
func KeepWindow() Strategy { return keepWindow{} }

type keepWindow struct{}

func (keepWindow) Compact(ctx context.Context, msgs []Message, cfg ContextConfig) ([]Message, error) {
	kept, _ := splitHistory(msgs, cfg.keepLast())
	return kept, nil
}

// Summarize keeps the recent window and replaces everything older with
// whatever fn returns, so the agent keeps the gist of a long thread.
// Returning no messages makes it a KeepWindow.
//
//	Strategy: kritrim.Summarize(func(ctx context.Context, dropped []kritrim.Message) ([]kritrim.Message, error) {
//		return []kritrim.Message{{Role: kritrim.RoleUser, Content: "Earlier: " + digest(dropped)}}, nil
//	})
func Summarize(fn func(ctx context.Context, dropped []Message) ([]Message, error)) Strategy {
	return summarize{fn: fn}
}

type summarize struct {
	fn func(ctx context.Context, dropped []Message) ([]Message, error)
}

func (s summarize) Compact(ctx context.Context, msgs []Message, cfg ContextConfig) ([]Message, error) {
	kept, dropped := splitHistory(msgs, cfg.keepLast())
	if len(dropped) == 0 || s.fn == nil {
		return kept, nil
	}
	repl, err := s.fn(ctx, dropped)
	if err != nil {
		return nil, err
	}
	return append(append([]Message(nil), repl...), kept...), nil
}

// SummarizeWith keeps the recent window and asks a model to summarize
// everything older. Pass instructions to steer what the summary must keep,
// or "" for the default. The summary call is a plain model call: it is not
// retried by Config.Retry, it does not run tools, and its tokens are not
// counted against the run budget.
//
// Use a small, cheap model here. The summary replaces history for the rest
// of the thread, so a model that drops details quietly makes the agent
// forget them.
func SummarizeWith(m Model, instructions string) Strategy {
	if instructions == "" {
		instructions = "Summarize this conversation for an assistant that must continue it. " +
			"Keep decisions, facts, names, numbers, file paths and anything still open. " +
			"Drop pleasantries. Write at most 200 words."
	}
	return Summarize(func(ctx context.Context, dropped []Message) ([]Message, error) {
		if m == nil {
			return nil, fmt.Errorf("kritrim: SummarizeWith needs a model")
		}
		resp, err := m.Generate(ctx, Request{
			Model:     modelName(m),
			System:    instructions,
			Messages:  []Message{{Role: RoleUser, Content: transcript(dropped)}},
			MaxTokens: 1024,
		})
		if err != nil {
			return nil, fmt.Errorf("kritrim: summarize context: %w", err)
		}
		return []Message{{Role: RoleUser, Content: "Summary of the earlier conversation:\n" + resp.Text}}, nil
	})
}

// transcript renders messages as plain text for a summarizer.
func transcript(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case RoleTool:
			fmt.Fprintf(&b, "tool %s: %s\n", m.ToolName, truncate(m.Content, 2000))
		default:
			fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
			for _, c := range m.ToolCalls {
				fmt.Fprintf(&b, "  called %s(%s)\n", c.Name, jsonString(c.Args))
			}
		}
	}
	return b.String()
}

// splitHistory cuts msgs into the messages to keep and the ones to drop. It
// only cuts where the result is a valid conversation: never at a tool
// result, and never at an assistant turn whose tool results would be
// orphaned.
func splitHistory(msgs []Message, keepLast int) (kept, dropped []Message) {
	start := len(msgs) - keepLast
	if start < 0 {
		start = 0
	}
	for start < len(msgs) && msgs[start].Role == RoleTool {
		start++
	}
	for start < len(msgs) && msgs[start].Role == RoleAssistant {
		start++
	}
	if start <= 0 {
		return msgs, nil
	}
	return append([]Message(nil), msgs[start:]...), append([]Message(nil), msgs[:start]...)
}

// trimContext applies the configured strategy when the history is over
// budget. Called before every model call.
func trimContext(ctx context.Context, msgs []Message, cfg *ContextConfig) ([]Message, error) {
	if cfg == nil || cfg.MaxTokens <= 0 || EstimateTokens(msgs) <= cfg.MaxTokens {
		return msgs, nil
	}
	s := cfg.Strategy
	if s == nil {
		s = KeepWindow()
	}
	out, err := s.Compact(ctx, msgs, *cfg)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return msgs, nil
	}
	return out, nil
}
