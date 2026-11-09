package kritrim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBedrockConverse(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Amz-Date") == "" {
			t.Error("missing x-amz-date")
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"hello"}]}},"stopReason":"end_turn","usage":{"inputTokens":3,"outputTokens":2}}`))
	}))
	defer srv.Close()
	m := Bedrock("test-model", ModelConfig{BaseURL: srv.URL, Region: "us-east-1", AccessKey: "AK", SecretKey: "SK"})
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
	if _, ok := body["messages"]; !ok {
		t.Fatalf("body %+v", body)
	}
	if _, ok := body["toolConfig"]; !ok {
		t.Fatalf("tools not sent: %+v", body)
	}
}

func TestBedrockToolUseAndSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"output":{"message":{"role":"assistant","content":[{"toolUse":{"toolUseId":"t1","name":"lookup","input":{"id":"7"}}}]}},"stopReason":"tool_use","usage":{"inputTokens":1,"outputTokens":1}}`))
	}))
	defer srv.Close()
	m := Bedrock("m", ModelConfig{BaseURL: srv.URL, AccessKey: "AK", SecretKey: "SK"})
	resp, err := m.Generate(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "lookup" || resp.ToolCalls[0].Args["id"] != "7" {
		t.Fatalf("%+v", resp.ToolCalls)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"output":{"message":{"role":"assistant","content":[{"toolUse":{"toolUseId":"t1","name":"out","input":{"name":"pancakes"}}}]}},"stopReason":"tool_use","usage":{}}`))
	}))
	defer srv2.Close()
	m2 := Bedrock("m", ModelConfig{BaseURL: srv2.URL, AccessKey: "AK", SecretKey: "SK"})
	resp2, err := m2.Generate(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
		Schema:   objectSchema(map[string]any{"name": map[string]any{"type": "string"}}), SchemaName: "out",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp2.Text, "pancakes") || len(resp2.ToolCalls) != 0 {
		t.Fatalf("%+v", resp2)
	}
}

func TestBedrockMissingCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	m := Bedrock("m", ModelConfig{Region: "us-east-1"})
	if _, err := m.Generate(context.Background(), Request{}); err == nil {
		t.Fatal("want credentials error")
	}
}
