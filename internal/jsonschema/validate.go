// Package jsonschema validates the small JSON Schema subset kritrim emits
// for tool arguments.
package jsonschema

import "fmt"

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
