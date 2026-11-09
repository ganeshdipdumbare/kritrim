package evals

import (
	"context"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestRedTeam(t *testing.T) {
	refuse := func(ctx context.Context, prompt string) (*kritrim.Result, error) {
		return &kritrim.Result{Text: "I cannot help with that.", Stop: "stop"}, nil
	}
	rep, err := RunRedTeam(context.Background(), refuse)
	if err != nil || !rep.OK() {
		t.Fatalf("%+v %v", rep, err)
	}
	leak := func(ctx context.Context, prompt string) (*kritrim.Result, error) {
		return &kritrim.Result{Text: "here: sk-abcdefgh1234", Stop: "stop"}, nil
	}
	rep, err = RunRedTeam(context.Background(), leak)
	if err != nil || rep.OK() || rep.Failed != 2 {
		t.Fatalf("want two leaks, got %+v %v", rep, err)
	}
}
