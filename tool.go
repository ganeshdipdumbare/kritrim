package kritrim

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/ganeshdipdumbare/kritrim/internal/jsonschema"
)

// Tool is one thing the model can call. Tools are built with NewTool; the
// callable is deliberately not exported, so there is one way to define one.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`

	run  func(ctx context.Context, args map[string]any) (string, error)
	meta map[string]any

	// err is a constructor-time problem, surfaced by New.
	err error
}

// NewTool defines a tool from a typed Go function. The argument schema is
// derived from In; a string Out is returned as is, any other Out is sent to
// the model as JSON.
//
//	func weather(ctx context.Context, in struct {
//		City string `json:"city" desc:"city name"`
//	}) (string, error) { return "sunny", nil }
//
//	weatherTool := kritrim.NewTool("weather", "Get the weather", weather)
func NewTool[In, Out any](name, description string, fn func(ctx context.Context, in In) (Out, error)) Tool {
	t := Tool{
		Name:        name,
		Description: description,
		Schema:      schemaOf(*new(In)),
	}
	if !validToolName.MatchString(name) {
		t.err = fmt.Errorf("kritrim: tool name %q must use letters, digits, '_' or '-'", name)
		return t
	}
	if fn == nil {
		t.err = fmt.Errorf("kritrim: tool %q has a nil function", name)
		return t
	}
	if err := jsonschema.Validate(t.Schema); err != nil {
		t.err = fmt.Errorf("kritrim: tool %q schema: %w", name, err)
		return t
	}
	t.run = func(ctx context.Context, args map[string]any) (string, error) {
		var in In
		raw, _ := json.Marshal(args)
		if err := json.Unmarshal(raw, &in); err != nil {
			return "", fmt.Errorf("invalid arguments for %s: %w", name, err)
		}
		out, err := fn(ctx, in)
		if err != nil {
			return "", err
		}
		if s, ok := any(out).(string); ok {
			return s, nil
		}
		b, err := json.Marshal(out)
		return string(b), err
	}
	return t
}

var validToolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// NewRawTool builds a tool from a JSON Schema and an untyped function. Use it
// for dynamic schemas (MCP servers, simulators); NewTool is the normal path.
func NewRawTool(name, description string, schema map[string]any, fn func(ctx context.Context, args map[string]any) (string, error)) Tool {
	t := Tool{Name: name, Description: description, Schema: schema}
	switch {
	case !validToolName.MatchString(name):
		t.err = fmt.Errorf("kritrim: tool name %q must use letters, digits, '_' or '-'", name)
	case fn == nil:
		t.err = fmt.Errorf("kritrim: tool %q has a nil function", name)
	case schema == nil:
		t.err = fmt.Errorf("kritrim: tool %q has a nil schema", name)
	default:
		if err := jsonschema.Validate(schema); err != nil {
			t.err = fmt.Errorf("kritrim: tool %q schema: %w", name, err)
			return t
		}
		t.run = fn
	}
	return t
}

// SchemaOf derives a JSON Schema from a Go type. NewTool uses it; it is
// exported for evaluators and advanced tools.
func SchemaOf(v any) map[string]any { return schemaOf(v) }

// Risky marks a tool so Config.Approve is asked before it runs.
func Risky(t Tool) Tool {
	t.meta = copyMeta(t.meta)
	t.meta["risk"] = "high"
	return t
}

func copyMeta(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (t Tool) risky() bool {
	v, _ := t.meta["risk"].(string)
	return v == "high"
}

// validate is called by New for every tool.
func (t Tool) validate() error {
	if t.err != nil {
		return t.err
	}
	if t.Name == "" || t.run == nil || t.Schema == nil {
		return fmt.Errorf("kritrim: tool %q is incomplete (name, schema and a function are required)", t.Name)
	}
	return jsonschema.Validate(t.Schema)
}

// checkArgs validates model-supplied arguments against the tool schema before
// the tool runs. A nil args map is treated as an empty object.
func (t Tool) checkArgs(args map[string]any) error {
	var v any = map[string]any{}
	if args != nil {
		v = args
	}
	if err := jsonschema.Value(t.Schema, v); err != nil {
		return fmt.Errorf("invalid arguments for %s: %w", t.Name, err)
	}
	return nil
}
