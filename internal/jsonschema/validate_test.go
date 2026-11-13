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

func TestConstraints(t *testing.T) {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"n":    map[string]any{"type": "number", "minimum": 1, "maximum": 10, "multipleOf": 0.5},
			"x":    map[string]any{"type": "integer", "exclusiveMinimum": 0},
			"code": map[string]any{"type": "string", "pattern": "^[A-Z]{2}-[0-9]+$", "minLength": 4, "maxLength": 8},
			"tags": map[string]any{"type": "array", "minItems": 1, "maxItems": 2, "uniqueItems": true},
			"kind": map[string]any{"const": "refund"},
			"id":   map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "integer"}}},
			"one":  map[string]any{"oneOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "number"}}},
			"meta": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		},
	}
	ok := []string{`{"n":1.5,"x":1,"code":"AB-12","tags":["a"],"kind":"refund","id":"x","one":1.5,"meta":{"k":"v"}}`, `{"id":3}`}
	bad := []string{`{"n":0}`, `{"n":11}`, `{"n":1.25}`, `{"x":0}`, `{"code":"ab-12"}`, `{"code":"AB-123456"}`,
		`{"tags":[]}`, `{"tags":["a","a"]}`, `{"tags":["a","b","c"]}`, `{"kind":"other"}`, `{"id":true}`,
		`{"one":1}`, `{"meta":{"k":1}}`}
	for _, js := range ok {
		var v any
		_ = json.Unmarshal([]byte(js), &v)
		if err := Value(s, v); err != nil {
			t.Errorf("%s: %v", js, err)
		}
	}
	for _, js := range bad {
		var v any
		_ = json.Unmarshal([]byte(js), &v)
		if Value(s, v) == nil {
			t.Errorf("%s accepted", js)
		}
	}
	// Constraints Value cannot enforce are refused, not skipped.
	for _, kw := range []string{"patternProperties", "if", "contains"} {
		if Value(map[string]any{"type": "object", kw: map[string]any{}}, map[string]any{}) == nil {
			t.Errorf("unsupported keyword %s silently ignored", kw)
		}
	}
	if Value(map[string]any{"$ref": "https://example.com/s.json"}, "x") == nil {
		t.Error("remote $ref silently ignored")
	}
	if Value(map[string]any{"type": "string", "pattern": "(?<=x)"}, "x") == nil {
		t.Error("uncompilable pattern silently ignored")
	}
}

func TestLocalRef(t *testing.T) {
	// The shape pydantic and FastMCP emit for nested models.
	s := map[string]any{
		"type": "object",
		"$defs": map[string]any{
			"Item": map[string]any{"type": "object", "properties": map[string]any{
				"sku": map[string]any{"type": "string"}, "qty": map[string]any{"type": "integer", "minimum": 1}},
				"required": []any{"sku"}},
		},
		"properties": map[string]any{"items": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/Item"}}},
	}
	var good, bad any
	_ = json.Unmarshal([]byte(`{"items":[{"sku":"a","qty":2}]}`), &good)
	_ = json.Unmarshal([]byte(`{"items":[{"qty":0}]}`), &bad)
	if err := Value(s, good); err != nil {
		t.Fatal(err)
	}
	if Value(s, bad) == nil {
		t.Fatal("invalid nested item accepted")
	}
	// A self-referencing schema terminates.
	loop := map[string]any{"$ref": "#"}
	if Value(loop, "x") == nil {
		t.Fatal("ref loop accepted")
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
