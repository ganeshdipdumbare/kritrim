package evals

import (
	"context"

	"github.com/ganeshdipdumbare/kritrim"
)

// SimulatedTools returns fixture-backed tools with the given names. Use them
// to test an agent without real side effects.
func SimulatedTools(fixtures map[string]string) []kritrim.Tool {
	schema := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
	out := make([]kritrim.Tool, 0, len(fixtures))
	for name, result := range fixtures {
		value := result
		out = append(out, kritrim.NewRawTool(name, "simulated "+name, schema,
			func(ctx context.Context, args map[string]any) (string, error) { return value, nil }))
	}
	return out
}
