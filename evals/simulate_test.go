package evals

import (
	"context"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestGenerateCases(t *testing.T) {
	model := kritrim.Mock(kritrim.Turn{Text: `{"cases":[{"name":"a","input":"hello","expected":"hi"}]}`})
	cases, err := GenerateCases(context.Background(), model, "a greeter", 3)
	if err != nil || len(cases) != 1 || cases[0].Name != "a" {
		t.Fatalf("%+v %v", cases, err)
	}
}

func TestSimulate(t *testing.T) {
	user := kritrim.Mock(
		kritrim.Turn{Text: "hello"},
		kritrim.Turn{Text: "DONE"},
	)
	agent := func(ctx context.Context, prompt string) (*kritrim.Result, error) {
		return &kritrim.Result{Text: "hi there", Stop: "stop"}, nil
	}
	r, err := Simulate(context.Background(), SimConfig{
		Model: user, Persona: "a new user", Goal: "get a greeting", MaxTurns: 4, Judge: NoError(),
	}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Pass || r.Output != "hi there" {
		t.Fatalf("%+v", r)
	}
}
