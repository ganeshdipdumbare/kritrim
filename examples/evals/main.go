// Evaluate an agent offline with deterministic evaluators.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/evals"
)

func main() {
	agent, err := kritrim.New(kritrim.Config{Model: kritrim.Mock(kritrim.Turn{Text: "Paris"})})
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()

	report, err := evals.Experiment{
		Cases: []evals.Case{
			{Name: "capital", Input: "Capital of France?", Expected: "Paris"},
		},
		Evaluators: []evals.Evaluator{evals.Contains(), evals.NoError()},
		Detect:     true,
	}.Run(context.Background(), func(ctx context.Context, c evals.Case) (*kritrim.Result, error) {
		return agent.Run(ctx, c.Input)
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(report.Summary())
}
