// Package jsonschema validates the small JSON Schema subset kritrim emits
// for tool arguments.
package jsonschema

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Validate checks that s is a usable object schema.
func Validate(s map[string]any) error {
	if s == nil {
		return fmt.Errorf("schema is nil")
	}
	t, _ := s["type"].(string)
	if t != "object" {
		return fmt.Errorf("top-level type must be object, got %q", t)
	}
	return check(s, "arguments")
}

func check(s map[string]any, path string) error {
	typ, _ := s["type"].(string)
	switch typ {
	case "", "object":
		if props, ok := s["properties"].(map[string]any); ok {
			for name, raw := range props {
				p, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.%s: property schema must be an object", path, name)
				}
				if err := check(p, path+"."+name); err != nil {
					return err
				}
			}
		}
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if _, ok := r.(string); !ok {
					return fmt.Errorf("%s: required entries must be strings", path)
				}
			}
		}
	case "array":
		if items, ok := s["items"].(map[string]any); ok {
			return check(items, path+"[]")
		}
	case "string", "integer", "number", "boolean":
		return nil
	default:
		return fmt.Errorf("%s: unsupported type %q", path, typ)
	}
	return nil
}

// Value checks v (decoded JSON) against schema s. It enforces the keywords
// kritrim and typical MCP servers use: type, properties, required,
// additionalProperties: false, items, enum. Unknown keywords are ignored so
// richer schemas stay usable.
func Value(s map[string]any, v any) error { return value(s, v, "arguments", 0) }

func value(s map[string]any, v any, path string, depth int) error {
	if s == nil {
		return nil
	}
	if depth > 64 {
		return fmt.Errorf("%s: nested too deeply", path)
	}
	if enum, ok := s["enum"].([]any); ok && len(enum) > 0 {
		found := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(v) && sameKind(e, v) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s: %v is not one of %v", path, v, enum)
		}
	}
	types := typesOf(s["type"])
	if len(types) > 0 {
		ok := false
		for _, t := range types {
			if isType(t, v) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%s: expected %s, got %s", path, strings.Join(types, " or "), kindOf(v))
		}
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		for _, r := range stringsOf(s["required"]) {
			if _, ok := x[r]; !ok {
				return fmt.Errorf("%s.%s is required", path, r)
			}
		}
		closed := s["additionalProperties"] == false
		for k, fv := range x {
			ps, ok := props[k].(map[string]any)
			if !ok {
				if closed {
					return fmt.Errorf("%s.%s is not allowed", path, k)
				}
				continue
			}
			if err := value(ps, fv, path+"."+k, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if items, ok := s["items"].(map[string]any); ok {
			for i, e := range x {
				if err := value(items, e, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func typesOf(t any) []string {
	switch x := t.(type) {
	case string:
		return []string{x}
	case []any:
		return stringsOf(x)
	case []string:
		return x
	}
	return nil
}

func stringsOf(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func isType(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		return isNumber(v)
	case "integer":
		switch n := v.(type) {
		case float64:
			return n == math.Trunc(n) && !math.IsInf(n, 0)
		case float32:
			return float64(n) == math.Trunc(float64(n))
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		case json.Number:
			_, err := n.Int64()
			return err == nil
		}
		return false
	}
	return true // unknown type keyword: do not reject
}

func isNumber(v any) bool {
	switch v.(type) {
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return true
	}
	return false
}

func sameKind(a, b any) bool {
	return kindOf(a) == kindOf(b) || (isNumber(a) && isNumber(b))
}

func kindOf(v any) string {
	switch {
	case v == nil:
		return "null"
	case isNumber(v):
		return "number"
	}
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return fmt.Sprintf("%T", v)
}
