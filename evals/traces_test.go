package evals

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/trace"
)

func TestFromTraceRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	lookup := kritrim.NewTool("lookup", "look up", func(ctx context.Context, in struct {
		ID string `json:"id"`
	}) (string, error) {
		return "order " + in.ID + " shipped", nil
	})
	a, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(kritrim.TurnTool("lookup", map[string]any{"id": "7"}), kritrim.Turn{Text: "Order 7 shipped."},
			kritrim.Turn{Text: "Hello."}),
		Tools: []kritrim.Tool{lookup},
		Hooks: trace.JSON(&buf, trace.Content()),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Run(context.Background(), "where is order 7?", kritrim.Thread("t1")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "hi", kritrim.Thread("t2")); err != nil {
		t.Fatal(err)
	}
	results, err := FromTrace(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}
	r := results[0]
	if r.Case.Input != "where is order 7?" || r.Output != "Order 7 shipped." || !strings.HasPrefix(r.Case.Name, "t1/") {
		t.Fatalf("%+v", r)
	}
	calls := r.Run.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "lookup" || calls[0].Args["id"] != "7" {
		t.Fatalf("calls %+v", calls)
	}
	rep := Experiment{Evaluators: []Evaluator{
		ToolCalls(ToolCallConfig{Require: []string{"lookup"}}),
		ToolParameters("lookup", func(args map[string]any) error { return nil }),
	}}.Score(context.Background(), results[:1])
	if !rep.OK() {
		t.Fatalf("report: %s", rep.Summary())
	}
}

func TestFromTraceWithoutContentAndTruncated(t *testing.T) {
	in := `{"event":"run.start","run":"r1","thread":"a","time":"2026-01-01T00:00:00Z"}
{"event":"model.end","run":"r1","in":3,"out":2,"tools":1}
{"event":"tool.end","run":"r1","tool":"shell","call":"c1","error":true}
{"event":"run.end","run":"r1","stop":"max_steps","time":"2026-01-01T00:00:02Z"}
{"event":"run.start","run":"r2"}
`
	results, err := FromTrace(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Run.Stop != "max_steps" || results[0].Tokens != 5 || results[0].Duration.Seconds() != 2 {
		t.Fatalf("%+v", results[0])
	}
	if results[1].Err == "" {
		t.Fatal("unfinished run not reported")
	}
	rep := Experiment{Evaluators: []Evaluator{MaxSteps()}, Detect: true}.Score(context.Background(), results)
	if rep.Failed != 2 {
		t.Fatalf("report: %s", rep.Summary())
	}
	if _, err := FromTrace(strings.NewReader("not json\n")); err == nil {
		t.Fatal("bad line accepted")
	}
}
