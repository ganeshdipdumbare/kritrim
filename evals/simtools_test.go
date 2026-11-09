package evals

import (
	"context"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestSimulatedTools(t *testing.T) {
	a, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(kritrim.TurnTool("lookup", nil), kritrim.Turn{Text: "done"}),
		Tools: SimulatedTools(map[string]string{"lookup": "42"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Steps[0].ToolResults[0].Content; got != "42" {
		t.Fatalf("%q", got)
	}
}
