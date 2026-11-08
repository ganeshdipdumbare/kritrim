package multiagent

import (
	"context"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestGraphBranch(t *testing.T) {
	g := NewGraph().Start("triage").
		Node("triage", func(ctx context.Context, prompt string, s *State) (*kritrim.Result, error) {
			s.Set("intent", "billing")
			return &kritrim.Result{Text: "billing", Stop: "stop"}, nil
		}).
		Node("billing", func(ctx context.Context, prompt string, s *State) (*kritrim.Result, error) {
			return &kritrim.Result{Text: "billing answer for " + prompt, Stop: "stop"}, nil
		}).
		Node("other", func(ctx context.Context, prompt string, s *State) (*kritrim.Result, error) {
			return &kritrim.Result{Text: "other", Stop: "stop"}, nil
		}).
		Edge("triage", "billing", func(s *State) bool { return s.Get("intent") == "billing" }).
		Edge("triage", "other", nil)
	res, err := g.Run(context.Background(), "invoice")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "billing answer") {
		t.Fatalf("%q", res.Text)
	}
}

func TestGraphCycleCap(t *testing.T) {
	g := NewGraph().Start("a").MaxSteps(5).
		Node("a", func(ctx context.Context, prompt string, s *State) (*kritrim.Result, error) {
			return &kritrim.Result{Text: "a", Stop: "stop"}, nil
		}).
		Edge("a", "a", nil)
	if _, err := g.Run(context.Background(), "x"); err == nil {
		t.Fatal("want cycle cap error")
	}
}

func TestSwarmHandoff(t *testing.T) {
	a, err := kritrim.New(kritrim.Config{Model: kritrim.Mock(
		kritrim.TurnTool("handoff_to_b", map[string]any{"task": "write it"}),
		kritrim.Turn{Text: "A done"},
	)})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := kritrim.New(kritrim.Config{Model: kritrim.Mock(kritrim.Turn{Text: "B answer"})})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s, err := NewSwarm(
		Member{Name: "a", Description: "triage", Agent: a},
		Member{Name: "b", Description: "writer", Agent: b},
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Run(context.Background(), "draft")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "B answer" {
		t.Fatalf("%q", res.Text)
	}
}
