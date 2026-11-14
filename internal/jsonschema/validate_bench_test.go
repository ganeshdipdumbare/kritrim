package jsonschema

import "testing"

func BenchmarkValue(b *testing.B) {
	s := map[string]any{"type": "object", "required": []any{"id"}, "additionalProperties": false,
		"properties": map[string]any{
			"id":     map[string]any{"type": "string", "minLength": float64(1), "pattern": "^[A-Z]-[0-9]+$"},
			"status": map[string]any{"type": "string", "enum": []any{"open", "closed"}},
			"tags":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"limit":  map[string]any{"type": "integer", "minimum": float64(1), "maximum": float64(100)},
		}}
	v := map[string]any{"id": "A-17", "status": "open", "tags": []any{"a", "b"}, "limit": float64(5)}
	b.ReportAllocs()
	for b.Loop() {
		if err := Value(s, v); err != nil {
			b.Fatal(err)
		}
	}
}
