package kritrim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIStream(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"Hel"}}]}

data: {"choices":[{"delta":{"content":"lo"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3}}

data: [DONE]

`))
	}))
	defer srv.Close()
	m := OpenAI("gpt-test", ModelConfig{BaseURL: srv.URL, APIKey: "key"})
	var deltas []string
	resp, err := m.Stream(context.Background(), Request{
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []Tool{{Name: "x", Description: "d", Schema: objectSchema(map[string]any{})}},
	}, func(d Delta) { deltas = append(deltas, d.Text) })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hello" || len(deltas) != 2 || resp.Usage.In != 2 || resp.Usage.Out != 3 {
		t.Fatalf("%+v deltas=%v", resp, deltas)
	}
	if body["stream"] != true || body["model"] != "gpt-test" {
		t.Fatalf("request %+v", body)
	}
	if _, ok := body["tools"]; !ok {
		t.Fatal("tools not sent")
	}
}

func TestOpenAIToolCallsAndSchema(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"lookup","arguments":"{\"id\":"}}]}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"7}"}}]}}]}
data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}
data: [DONE]
`))
	}))
	defer srv.Close()
	m := OpenAI("m", ModelConfig{BaseURL: srv.URL})
	resp, err := m.Stream(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}},
		Schema: map[string]any{"type": "object", "properties": map[string]any{}}, SchemaName: "out"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "lookup" || resp.ToolCalls[0].Args["id"] != float64(7) {
		t.Fatalf("%+v", resp.ToolCalls)
	}
	rf, _ := body["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Fatalf("schema not requested: %+v", body["response_format"])
	}
}

func TestAnthropicStream(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "key" {
			t.Errorf("key header %q", r.Header.Get("x-api-key"))
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`))
	}))
	defer srv.Close()
	m := Anthropic("claude", ModelConfig{BaseURL: srv.URL, APIKey: "key"})
	var got string
	resp, err := m.Stream(context.Background(), Request{
		System:   "s",
		Messages: []Message{{Role: RoleUser, Content: "x"}},
		Tools:    []Tool{{Name: "t", Description: "d", Schema: objectSchema(map[string]any{})}},
	}, func(d Delta) { got += d.Text })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hi" || got != "Hi" || resp.Usage.In != 5 || resp.Usage.Out != 2 {
		t.Fatalf("%+v %q", resp, got)
	}
	if body["system"] != "s" {
		t.Fatalf("system %+v", body["system"])
	}
	if _, ok := body["tools"]; !ok {
		t.Fatal("tools not sent")
	}
}

func TestAnthropicSchema(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":1}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"out"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"name\":\"pancakes\"}"}}

data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}

`))
	}))
	defer srv.Close()
	m := Anthropic("claude", ModelConfig{BaseURL: srv.URL})
	resp, err := m.Stream(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}},
		Schema:     map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}},
		SchemaName: "out"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "pancakes") || len(resp.ToolCalls) != 0 {
		t.Fatalf("%+v", resp)
	}
}

func TestGeminiStream(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !strings.Contains(r.URL.Path, "streamGenerateContent") {
			t.Errorf("path %q", r.URL.Path)
		}
		w.Write([]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"He"}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"llo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}

`))
	}))
	defer srv.Close()
	m := Gemini("gemini-test", ModelConfig{BaseURL: srv.URL, APIKey: "key"})
	var got string
	resp, err := m.Stream(context.Background(), Request{
		System:   "s",
		Messages: []Message{{Role: RoleUser, Content: "x"}},
		Tools:    []Tool{{Name: "t", Description: "d", Schema: objectSchema(map[string]any{"q": map[string]any{"type": "string"}}, "q")}},
	}, func(d Delta) { got += d.Text })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hello" || got != "Hello" || resp.Usage.In != 3 {
		t.Fatalf("%+v %q", resp, got)
	}
	if body["systemInstruction"] == nil {
		t.Fatal("system instruction missing")
	}
}

func TestGeminiSchemaUppercase(t *testing.T) {
	m := &geminiModel{name: "g", cfg: ModelConfig{APIKey: "k"}}
	req := m.request(Request{Schema: objectSchema(map[string]any{"name": map[string]any{"type": "string"}}, "name")})
	if req.GenerationConfig == nil || req.GenerationConfig.ResponseMime != "application/json" {
		t.Fatal("schema not requested")
	}
	if req.GenerationConfig.ResponseSchema["type"] != "OBJECT" {
		t.Fatalf("schema %+v", req.GenerationConfig.ResponseSchema)
	}
}
