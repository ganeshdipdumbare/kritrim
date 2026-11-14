package multiagent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/multiagent"
)

func agentSaying(t *testing.T, turns ...kritrim.Turn) *kritrim.Agent {
	t.Helper()
	a, err := kritrim.New(kritrim.Config{Model: kritrim.Mock(turns...)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func TestGraphAsTool(t *testing.T) {
	billing := agentSaying(t, kritrim.Turn{Text: "invoice 42 is paid"})
	g := multiagent.NewGraph().Start("billing").
		Node("billing", func(ctx context.Context, prompt string, st *multiagent.State) (*kritrim.Result, error) {
			return billing.Run(ctx, prompt)
		})
	tool := g.AsTool("billing_desk", "Answers billing questions")

	front, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(
			kritrim.TurnTool("billing_desk", map[string]any{"task": "is invoice 42 paid?"}),
			kritrim.Turn{Text: "Yes, it is paid."},
		),
		Tools: []kritrim.Tool{tool},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	res, err := front.Run(context.Background(), "invoice 42?")
	if err != nil {
		t.Fatal(err)
	}
	got := res.Steps[0].ToolResults[0]
	if got.IsError || !strings.Contains(got.Content, "invoice 42 is paid") {
		t.Fatalf("tool result = %+v", got)
	}
}

func TestSwarmAsTool(t *testing.T) {
	writer := agentSaying(t, kritrim.Turn{Text: "the post is written"})
	s, err := multiagent.NewSwarm(multiagent.Member{Name: "writer", Description: "writes", Agent: writer})
	if err != nil {
		t.Fatal(err)
	}
	tool := s.AsTool("writing_team", "Writes copy")
	out, err := callTool(t, tool, map[string]any{"task": "draft the launch post"})
	if err != nil || !strings.Contains(out, "the post is written") {
		t.Fatalf("%q %v", out, err)
	}
}

// TestAsToolReportsAnInterrupt: a paused sub-agent cannot be resumed from
// inside a tool call, so the tool says so instead of pretending to finish.
func TestAsToolReportsAnInterrupt(t *testing.T) {
	refund := kritrim.Risky(kritrim.NewTool("refund", "refund", func(ctx context.Context, in struct {
		ID string `json:"id"`
	}) (string, error) {
		return "refunded", nil
	}))
	sub, err := kritrim.New(kritrim.Config{
		Model:   kritrim.Mock(kritrim.TurnTool("refund", map[string]any{"id": "A-17"})),
		Tools:   []kritrim.Tool{refund},
		Session: kritrim.MemorySessions(),
		Approve: func(ctx context.Context, req kritrim.Approval) (kritrim.Decision, error) {
			return kritrim.Pause(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	g := multiagent.NewGraph().Start("refunds").
		Node("refunds", func(ctx context.Context, prompt string, st *multiagent.State) (*kritrim.Result, error) {
			return sub.Run(ctx, prompt, kritrim.Thread("ticket-1"))
		})
	_, err = callTool(t, g.AsTool("refund_desk", "Issues refunds"), map[string]any{"task": "refund A-17"})
	if err == nil || !strings.Contains(err.Error(), "approval") {
		t.Fatalf("want an approval error, got %v", err)
	}
}

// callTool runs a tool through an agent, which is the only way to get the
// run context the tool needs.
func callTool(t *testing.T, tool kritrim.Tool, args map[string]any) (string, error) {
	t.Helper()
	a, err := kritrim.New(kritrim.Config{Model: kritrim.Mock(), Tools: []kritrim.Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	return a.CallTool(context.Background(), tool.Name, args)
}
