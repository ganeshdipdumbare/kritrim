package kritrim

import (
	"context"
	"testing"
)

func tContext() context.Context { return context.Background() }

func TestSchemaTagsAndRequired(t *testing.T) {
	type in struct {
		City string   `json:"city" desc:"city name"`
		Unit string   `json:"unit" enum:"c,f"`
		Days int      `json:"days,omitempty"`
		Tags []string `json:"tags,omitempty"`
	}
	s := schemaOf(in{})
	props := s["properties"].(map[string]any)
	if props["city"].(map[string]any)["description"] != "city name" {
		t.Fatal("desc tag lost")
	}
	if _, ok := props["unit"].(map[string]any)["enum"]; !ok {
		t.Fatal("enum tag lost")
	}
	req := s["required"].([]any)
	if len(req) != 2 {
		t.Fatalf("required = %v", req)
	}
	for _, r := range req {
		if r == "days" || r == "tags" {
			t.Fatalf("omitempty field required: %v", req)
		}
	}
}

func TestUnknownToolResult(t *testing.T) {
	m := &spyModel{turns: []Turn{TurnTool("missing", nil), {Text: "ok"}}}
	a := newAgent(t, Config{Model: m})
	res, err := a.Run(tContext(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Steps[0].ToolResults[0]; !got.IsError {
		t.Fatalf("%+v", got)
	}
}

func TestOutputPointerRequired(t *testing.T) {
	a := newAgent(t, Config{Model: Mock()})
	var out struct{ X int }
	if _, err := a.Run(tContext(), "x", Output(out)); err == nil {
		t.Fatal("want pointer error")
	}
}
