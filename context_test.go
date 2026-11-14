package kritrim

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func history(n int) []Message {
	var out []Message
	for i := 0; i < n; i++ {
		role := RoleUser
		if i%2 == 1 {
			role = RoleAssistant
		}
		out = append(out, Message{Role: role, Content: fmt.Sprintf("message %d %s", i, strings.Repeat("x", 100))})
	}
	return out
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(nil); got != 0 {
		t.Fatalf("empty = %d", got)
	}
	// Four characters per token, plus per-message overhead: the estimate
	// grows with the content and is never zero for a real message.
	one := EstimateTokens([]Message{{Role: RoleUser, Content: strings.Repeat("x", 400)}})
	if one < 100 || one > 120 {
		t.Fatalf("400 characters = %d tokens", one)
	}
	if two := EstimateTokens(history(2)); two <= EstimateTokens(history(1)) {
		t.Fatal("estimate did not grow with the history")
	}
	// Tool calls and images cost something.
	withCall := EstimateTokens([]Message{{Role: RoleAssistant,
		ToolCalls: []ToolCall{{Name: "lookup", Args: map[string]any{"id": "A-17"}}}}})
	if withCall <= 8 {
		t.Fatalf("a tool call cost %d tokens", withCall)
	}
	withImage := EstimateTokens([]Message{{Role: RoleUser, Images: []Image{{Data: make([]byte, 5000)}}}})
	if withImage < 100 {
		t.Fatalf("an image cost %d tokens", withImage)
	}
}

// TestContextDoesNothingUnderBudget: compaction is for a history that no
// longer fits, and must not touch one that does.
func TestContextDoesNothingUnderBudget(t *testing.T) {
	msgs := history(10)
	for _, cfg := range []*ContextConfig{nil, {}, {MaxTokens: 1_000_000, KeepLast: 2}} {
		got, err := trimContext(context.Background(), msgs, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(msgs) {
			t.Fatalf("%+v trimmed %d messages to %d", cfg, len(msgs), len(got))
		}
	}
}

func TestKeepWindow(t *testing.T) {
	msgs := history(20)
	got, err := trimContext(context.Background(), msgs, &ContextConfig{MaxTokens: 100, KeepLast: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("kept %d messages, want 4", len(got))
	}
	if got[len(got)-1].Content != msgs[len(msgs)-1].Content {
		t.Fatal("the window did not keep the newest message")
	}
	// The default strategy is KeepWindow, so an explicit one matches.
	same, err := trimContext(context.Background(), msgs, &ContextConfig{MaxTokens: 100, KeepLast: 4, Strategy: KeepWindow()})
	if err != nil || len(same) != len(got) {
		t.Fatalf("explicit KeepWindow kept %d: %v", len(same), err)
	}
}

// TestCompactionKeepsAValidConversation: a cut that leaves a dangling tool
// result is rejected by every provider, so the window moves past it.
func TestCompactionKeepsAValidConversation(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "old question " + strings.Repeat("x", 400)},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "lookup"}}},
		{Role: RoleTool, ToolCallID: "c1", ToolName: "lookup", Content: "result"},
		{Role: RoleAssistant, Content: "the answer"},
		{Role: RoleUser, Content: "new question"},
	}
	// KeepLast 3 would start at the tool result; the cut must move past it
	// and past the assistant turn that asked for it.
	got, err := trimContext(context.Background(), msgs, &ContextConfig{MaxTokens: 10, KeepLast: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Role == RoleTool {
		t.Fatalf("history starts with %+v", got[0])
	}
	for _, m := range got {
		if m.Role == RoleTool {
			t.Fatalf("kept an orphaned tool result: %+v", got)
		}
	}
}

func TestSummarize(t *testing.T) {
	msgs := history(20)
	var dropped int
	got, err := trimContext(context.Background(), msgs, &ContextConfig{
		MaxTokens: 100, KeepLast: 4,
		Strategy: Summarize(func(ctx context.Context, d []Message) ([]Message, error) {
			dropped = len(d)
			return []Message{{Role: RoleUser, Content: "earlier: we talked"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 16 || len(got) != 5 || got[0].Content != "earlier: we talked" {
		t.Fatalf("dropped=%d kept=%d first=%q", dropped, len(got), got[0].Content)
	}
	// A failing summarizer stops the run rather than silently sending the
	// whole history to the provider.
	_, err = trimContext(context.Background(), msgs, &ContextConfig{
		MaxTokens: 100, KeepLast: 4,
		Strategy: Summarize(func(ctx context.Context, d []Message) ([]Message, error) {
			return nil, errors.New("summarizer down")
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "summarizer down") {
		t.Fatalf("err = %v", err)
	}
}

func TestSummarizeWith(t *testing.T) {
	var saw string
	m := &capturingSummarizer{reply: "They discussed messages 0 to 15.", saw: &saw}
	msgs := history(20)
	got, err := trimContext(context.Background(), msgs, &ContextConfig{
		MaxTokens: 100, KeepLast: 4, Strategy: SummarizeWith(m, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[0].Content, "They discussed messages") {
		t.Fatalf("first message = %q", got[0].Content)
	}
	if len(got) != 5 {
		t.Fatalf("kept %d messages", len(got))
	}
	// The summarizer saw a readable transcript of what was dropped.
	if !strings.Contains(saw, "message 0") || !strings.Contains(saw, "user:") {
		t.Fatalf("transcript = %q", truncate(saw, 200))
	}
	// A nil model is an error, not a silent pass-through.
	if _, err := trimContext(context.Background(), msgs, &ContextConfig{
		MaxTokens: 100, KeepLast: 4, Strategy: SummarizeWith(nil, ""),
	}); err == nil {
		t.Fatal("nil model accepted")
	}
}

// TestCustomStrategy: the interface is the extension point.
func TestCustomStrategy(t *testing.T) {
	got, err := trimContext(context.Background(), history(20), &ContextConfig{
		MaxTokens: 100, Strategy: firstAndLast{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("kept %d messages", len(got))
	}
}

type firstAndLast struct{}

func (firstAndLast) Compact(ctx context.Context, msgs []Message, cfg ContextConfig) ([]Message, error) {
	if len(msgs) < 2 {
		return msgs, nil
	}
	return []Message{msgs[0], msgs[len(msgs)-1]}, nil
}

// TestCompactionInARun checks the loop compacts what it sends and keeps the
// full history in the session: compaction is a prompt decision, not a
// deletion.
func TestCompactionInARun(t *testing.T) {
	var sent int
	a := newAgent(t, Config{
		Model:   &sizeModel{sent: &sent},
		Session: MemorySessions(),
		Context: &ContextConfig{MaxTokens: 50, KeepLast: 2},
	})
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		if _, err := a.Run(ctx, fmt.Sprintf("question %d %s", i, strings.Repeat("y", 200)), Thread("t1")); err != nil {
			t.Fatal(err)
		}
	}
	if sent > 4 {
		t.Fatalf("last call sent %d messages despite the budget", sent)
	}
	sess, err := a.Session(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Messages) != 12 {
		t.Fatalf("the session kept %d messages, want all 12", len(sess.Messages))
	}
}

type capturingSummarizer struct {
	reply string
	saw   *string
}

func (m *capturingSummarizer) Generate(ctx context.Context, req Request) (*Response, error) {
	if len(req.Messages) > 0 {
		*m.saw = req.Messages[0].Content
	}
	return &Response{Text: m.reply, Stop: "stop"}, nil
}

func (m *capturingSummarizer) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	return m.Generate(ctx, req)
}

type sizeModel struct{ sent *int }

func (m *sizeModel) Generate(ctx context.Context, req Request) (*Response, error) {
	*m.sent = len(req.Messages)
	return &Response{Text: "ok", Stop: "stop"}, nil
}

func (m *sizeModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	return m.Generate(ctx, req)
}
