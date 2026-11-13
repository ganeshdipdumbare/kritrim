package jsonschema

import (
	"encoding/json"
	"testing"
)

var testSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"n":    map[string]any{"type": "integer"},
		"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"mode": map[string]any{"type": "string", "enum": []any{"a", "b"}},
		"any":  map[string]any{"type": []any{"string", "null"}},
	},
	"required":             []any{"n"},
	"additionalProperties": false,
}

func TestValue(t *testing.T) {
	ok := []string{`{"n":1}`, `{"n":2,"tags":["x"],"mode":"a","any":null}`, `{"n":3,"any":"s"}`}
	bad := []string{`{}`, `{"n":"1"}`, `{"n":1.5}`, `{"n":1,"tags":[1]}`, `{"n":1,"mode":"c"}`, `{"n":1,"x":1}`, `{"n":1,"any":3}`, `[]`}
	for _, s := range ok {
		var v any
		_ = json.Unmarshal([]byte(s), &v)
		if err := Value(testSchema, v); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range bad {
		var v any
		_ = json.Unmarshal([]byte(s), &v)
		if Value(testSchema, v) == nil {
			t.Errorf("%s accepted", s)
		}
	}
}

func FuzzValue(f *testing.F) {
	f.Add(`{"n":1,"tags":["a"]}`)
	f.Add(`{"n":[[[[[]]]]]}`)
	f.Fuzz(func(t *testing.T, s string) {
		var v any
		if json.Unmarshal([]byte(s), &v) != nil {
			return
		}
		_ = Value(testSchema, v) // must not panic
	})
}
