package multiagent

import (
	"context"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func textNode(f func(string) string) Node {
	return func(ctx context.Context, prompt string, _ *State) (*kritrim.Result, error) {
		return &kritrim.Result{Text: f(prompt), Stop: kritrim.StopDone}, nil
	}
}

func TestSequence(t *testing.T) {
	g, err := Sequence(
		Step{"draft", textNode(func(p string) string { return "draft(" + p + ")" })},
		Step{"review", textNode(func(p string) string { return "review(" + p + ")" })},
		Step{"polish", textNode(strings.ToUpper)},
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Run(context.Background(), "post")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "REVIEW(DRAFT(POST))" {
		t.Fatalf("got %q", res.Text)
	}
	if _, err := Sequence(); err == nil {
		t.Fatal("empty sequence accepted")
	}
	if _, err := Sequence(Step{"a", textNode(strings.ToUpper)}, Step{"a", textNode(strings.ToUpper)}); err == nil {
		t.Fatal("duplicate step accepted")
	}
}

func TestRouter(t *testing.T) {
	classify := func(ctx context.Context, prompt string) (string, error) {
		if strings.Contains(prompt, "invoice") {
			return "billing", nil
		}
		if strings.Contains(prompt, "weather") {
			return "nowhere", nil
		}
		return "tech", nil
	}
	g, err := Router(classify, map[string]Node{
		"billing": textNode(func(p string) string { return "billing got: " + p }),
		"tech":    textNode(func(p string) string { return "tech got: " + p }),
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Run(context.Background(), "my invoice is wrong")
	if err != nil || res.Text != "billing got: my invoice is wrong" {
		t.Fatalf("%v %v", res, err)
	}
	res, _ = g.Run(context.Background(), "app crashes")
	if res.Text != "tech got: app crashes" {
		t.Fatalf("got %q", res.Text)
	}
	if _, err := g.Run(context.Background(), "weather?"); err == nil || !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("unknown route: %v", err)
	}
}

func TestAgentNode(t *testing.T) {
	a, _ := kritrim.New(kritrim.Config{Model: kritrim.Mock(kritrim.Turn{Text: "from agent"})})
	defer a.Close()
	g, _ := Sequence(Step{"only", AgentNode(a)})
	res, err := g.Run(context.Background(), "x")
	if err != nil || res.Text != "from agent" {
		t.Fatalf("%v %v", res, err)
	}
}
