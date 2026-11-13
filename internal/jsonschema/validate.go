// Package jsonschema validates the small JSON Schema subset kritrim emits
// for tool arguments.
package jsonschema

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
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

// Value checks v (decoded JSON) against schema s. It enforces type, enum,
// const, properties, required, additionalProperties (false or a schema),
// items, minItems/maxItems/uniqueItems, minProperties/maxProperties,
// minLength/maxLength/pattern, minimum/maximum/exclusiveMinimum/
// exclusiveMaximum/multipleOf, allOf/anyOf/oneOf/not, and local $ref
// ("#/$defs/..."), as pydantic and most MCP servers emit. A schema using a
// keyword that would constrain values but that Value cannot enforce
// (remote $ref, if/then/else, dependentSchemas, patternProperties,
// propertyNames, contains, prefixItems, unevaluated*) is rejected rather
// than silently not enforced. Annotations such as format, title and
// description are ignored, as the JSON Schema spec allows.
func Value(s map[string]any, v any) error { return value(s, s, v, "arguments", 0) }

// resolveRef follows a local JSON pointer such as "#/$defs/Item". Remote
// references are not fetched.
func resolveRef(root map[string]any, ref string) (map[string]any, error) {
	if ref == "#" {
		return root, nil
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("schema $ref %q is not local", ref)
	}
	var cur any = root
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema $ref %q does not resolve", ref)
		}
		if cur, ok = m[part]; !ok {
			return nil, fmt.Errorf("schema $ref %q does not resolve", ref)
		}
	}
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema $ref %q does not resolve to a schema", ref)
	}
	return m, nil
}

// unsupported lists constraining keywords Value does not implement.
var unsupported = []string{"$dynamicRef", "if", "then", "else", "dependentSchemas",
	"dependentRequired", "dependencies", "patternProperties", "propertyNames", "contains",
	"minContains", "maxContains", "prefixItems", "unevaluatedItems", "unevaluatedProperties"}

func value(root, s map[string]any, v any, path string, depth int) error {
	if s == nil {
		return nil
	}
	if depth > 64 {
		return fmt.Errorf("%s: nested too deeply", path)
	}
	if ref, ok := s["$ref"].(string); ok {
		target, err := resolveRef(root, ref)
		if err != nil {
			return fmt.Errorf("%s: %v, so the arguments cannot be validated", path, err)
		}
		if err := value(root, target, v, path, depth+1); err != nil {
			return err
		}
	}
	for _, k := range unsupported {
		if _, ok := s[k]; ok {
			return fmt.Errorf("%s: schema keyword %q is not supported, so the arguments cannot be validated", path, k)
		}
	}
	if c, ok := s["const"]; ok && !equalJSON(c, v) {
		return fmt.Errorf("%s: must be %v", path, c)
	}
	if err := combinators(root, s, v, path, depth); err != nil {
		return err
	}
	if err := scalarConstraints(s, v, path); err != nil {
		return err
	}
	if enum, ok := s["enum"].([]any); ok && len(enum) > 0 {
		found := false
		for _, e := range enum {
			if equalJSON(e, v) {
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
		if n, ok := num(s["minProperties"]); ok && float64(len(x)) < n {
			return fmt.Errorf("%s: needs at least %v properties", path, n)
		}
		if n, ok := num(s["maxProperties"]); ok && float64(len(x)) > n {
			return fmt.Errorf("%s: allows at most %v properties", path, n)
		}
		closed := s["additionalProperties"] == false
		extra, _ := s["additionalProperties"].(map[string]any)
		for k, fv := range x {
			ps, ok := props[k].(map[string]any)
			if !ok {
				if closed {
					return fmt.Errorf("%s.%s is not allowed", path, k)
				}
				if extra != nil {
					if err := value(root, extra, fv, path+"."+k, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			if err := value(root, ps, fv, path+"."+k, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if n, ok := num(s["minItems"]); ok && float64(len(x)) < n {
			return fmt.Errorf("%s: needs at least %v items", path, n)
		}
		if n, ok := num(s["maxItems"]); ok && float64(len(x)) > n {
			return fmt.Errorf("%s: allows at most %v items", path, n)
		}
		if s["uniqueItems"] == true {
			for i := range x {
				for j := i + 1; j < len(x); j++ {
					if equalJSON(x[i], x[j]) {
						return fmt.Errorf("%s: items must be unique", path)
					}
				}
			}
		}
		if _, isList := s["items"].([]any); isList {
			return fmt.Errorf("%s: tuple-form items is not supported, so the arguments cannot be validated", path)
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, e := range x {
				if err := value(root, items, e, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
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

func combinators(root, s map[string]any, v any, path string, depth int) error {
	if list, ok := s["allOf"].([]any); ok {
		for _, sub := range list {
			if m, ok := sub.(map[string]any); ok {
				if err := value(root, m, v, path, depth+1); err != nil {
					return err
				}
			}
		}
	}
	if list, ok := s["anyOf"].([]any); ok && len(list) > 0 {
		var first error
		matched := false
		for _, sub := range list {
			m, _ := sub.(map[string]any)
			err := value(root, m, v, path, depth+1)
			if err == nil {
				matched = true
				break
			}
			if first == nil {
				first = err
			}
		}
		if !matched {
			return fmt.Errorf("%s: matches none of anyOf (%v)", path, first)
		}
	}
	if list, ok := s["oneOf"].([]any); ok && len(list) > 0 {
		n := 0
		for _, sub := range list {
			m, _ := sub.(map[string]any)
			if value(root, m, v, path, depth+1) == nil {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("%s: must match exactly one of oneOf (matched %d)", path, n)
		}
	}
	if m, ok := s["not"].(map[string]any); ok && value(root, m, v, path, depth+1) == nil {
		return fmt.Errorf("%s: must not match the \"not\" schema", path)
	}
	return nil
}

func scalarConstraints(s map[string]any, v any, path string) error {
	if str, ok := v.(string); ok {
		n := float64(utf8.RuneCountInString(str))
		if m, ok := num(s["minLength"]); ok && n < m {
			return fmt.Errorf("%s: shorter than %v characters", path, m)
		}
		if m, ok := num(s["maxLength"]); ok && n > m {
			return fmt.Errorf("%s: longer than %v characters", path, m)
		}
		if p, ok := s["pattern"].(string); ok {
			re, err := compilePattern(p)
			if err != nil {
				return fmt.Errorf("%s: schema pattern %q cannot be enforced: %v", path, p, err)
			}
			if !re.MatchString(str) {
				return fmt.Errorf("%s: does not match pattern %q", path, p)
			}
		}
	}
	if x, ok := num(v); ok && isNumber(v) {
		if m, ok := num(s["minimum"]); ok && x < m {
			return fmt.Errorf("%s: below minimum %v", path, m)
		}
		if m, ok := num(s["maximum"]); ok && x > m {
			return fmt.Errorf("%s: above maximum %v", path, m)
		}
		if m, ok := num(s["exclusiveMinimum"]); ok && x <= m {
			return fmt.Errorf("%s: must be greater than %v", path, m)
		}
		if m, ok := num(s["exclusiveMaximum"]); ok && x >= m {
			return fmt.Errorf("%s: must be less than %v", path, m)
		}
		if m, ok := num(s["multipleOf"]); ok && m > 0 {
			q := x / m
			if math.Abs(q-math.Round(q)) > 1e-9 {
				return fmt.Errorf("%s: must be a multiple of %v", path, m)
			}
		}
	}
	return nil
}

var (
	patMu    sync.Mutex
	patCache = map[string]*regexp.Regexp{}
)

func compilePattern(p string) (*regexp.Regexp, error) {
	patMu.Lock()
	defer patMu.Unlock()
	if re, ok := patCache[p]; ok {
		return re, nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	if len(patCache) < 1024 {
		patCache[p] = re
	}
	return re, nil
}

func num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func equalJSON(a, b any) bool {
	if x, ok := num(a); ok {
		y, ok := num(b)
		return ok && x == y
	}
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ab) == string(bb)
}
