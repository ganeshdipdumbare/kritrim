package kritrim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIResponsesGenerate(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	defer srv.Close()
	m := OpenAIResponses("gpt-5", ModelConfig{BaseURL: srv.URL, APIKey: "key"})
	resp, err := m.Generate(context.Background(), Request{
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []Tool{{Name: "lookup", Description: "d", Schema: objectSchema(map[string]any{})}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello" || resp.Usage.In != 3 || resp.Usage.Out != 2 {
		t.Fatalf("%+v", resp)
	}
	if body["instructions"] != "be brief" {
		t.Fatalf("body %+v", body)
	}
	if _, ok := body["tools"]; !ok {
		t.Fatal("tools missing")
	}
}

func TestOpenAIResponsesToolCalls(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{\"id\":7}"}],"usage":{}}`))
	}))
	defer srv.Close()
	m := OpenAIResponses("gpt-5", ModelConfig{BaseURL: srv.URL})
	resp, err := m.Generate(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Content: "x"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c0", Name: "lookup", Args: map[string]any{"id": 1}}}},
			{Role: RoleTool, ToolCallID: "c0", Content: "one"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "lookup" || resp.ToolCalls[0].Args["id"] != float64(7) {
		t.Fatalf("%+v", resp.ToolCalls)
	}
	input, _ := body["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input %+v", body["input"])
	}
	last, _ := input[2].(map[string]any)
	if last["type"] != "function_call_output" || last["call_id"] != "c0" {
		t.Fatalf("last input %+v", last)
	}
}

func TestOpenAIResponsesStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"Hel"}

data: {"type":"response.output_text.delta","delta":"lo"}

data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}

`))
	}))
	defer srv.Close()
	m := OpenAIResponses("gpt-5", ModelConfig{BaseURL: srv.URL})
	var got string
	resp, err := m.Stream(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}},
		func(d Delta) { got += d.Text })
	if err != nil || resp.Text != "Hello" || got != "Hello" {
		t.Fatalf("%+v %q %v", resp, got, err)
	}
}

func TestOpenAIResponsesSchema(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{}"}]}],"usage":{}}`))
	}))
	defer srv.Close()
	m := OpenAIResponses("gpt-5", ModelConfig{BaseURL: srv.URL})
	if _, err := m.Generate(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
		Schema:   objectSchema(map[string]any{"name": map[string]any{"type": "string"}}), SchemaName: "out",
	}); err != nil {
		t.Fatal(err)
	}
	text, _ := body["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if format["type"] != "json_schema" || format["name"] != "out" {
		t.Fatalf("format %+v", format)
	}
	if format["schema"] == nil {
		t.Fatal("schema missing")
	}
}
