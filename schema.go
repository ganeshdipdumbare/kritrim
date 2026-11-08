package kritrim

import (
	"reflect"
	"strings"
)

// schemaOf derives a JSON Schema from a Go type. Field tags:
//
//	json:"name,omitempty"   name; omitempty or a pointer makes it optional
//	desc:"..."              description
//	enum:"a,b,c"            allowed values
func schemaOf(v any) map[string]any {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return map[string]any{"type": "object"}
	}
	if t.Kind() != reflect.Struct {
		return typeSchema(t)
	}
	props := map[string]any{}
	var required []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, opt := jsonName(f)
		if name == "-" {
			continue
		}
		s := typeSchema(f.Type)
		if d := f.Tag.Get("desc"); d != "" {
			s["description"] = d
		}
		if e := f.Tag.Get("enum"); e != "" {
			vals := strings.Split(e, ",")
			any := make([]any, len(vals))
			for j, x := range vals {
				any[j] = strings.TrimSpace(x)
			}
			s["enum"] = any
		}
		props[name] = s
		if !opt && f.Type.Kind() != reflect.Pointer && !strings.Contains(f.Tag.Get("json"), ",omitempty") {
			required = append(required, name)
		}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		any := make([]any, len(required))
		for i, r := range required {
			any[i] = r
		}
		s["required"] = any
	}
	return s
}

func jsonName(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("json")
	if tag == "" {
		return strings.ToLower(f.Name), false
	}
	parts := strings.Split(tag, ",")
	opt := false
	for _, p := range parts[1:] {
		if p == "omitempty" {
			opt = true
		}
	}
	return parts[0], opt
}

func typeSchema(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": typeSchema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object"}
	case reflect.Struct:
		return schemaOf(reflect.New(t).Interface())
	default:
		return map[string]any{"type": "string"}
	}
}

// objectSchema wraps field schemas as an object schema.
func objectSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		any := make([]any, len(required))
		for i, r := range required {
			any[i] = r
		}
		s["required"] = any
	}
	return s
}
