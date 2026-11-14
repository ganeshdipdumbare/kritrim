package kritrim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBedrockGuardrailConfig(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"Sorry, I can't discuss that."}]}},"stopReason":"guardrail_intervened","usage":{}}`))
	}))
	defer srv.Close()
	m := Bedrock("m", ModelConfig{BaseURL: srv.URL, AccessKey: "AK", SecretKey: "SK",
		Guardrail: &Guardrail{ID: "gr1", Version: "3", Trace: true}})
	a := newAgent(t, Config{Model: m, Tools: []Tool{echoTool("echo", nil)}})
	res, err := a.Run(context.Background(), "forbidden topic")
	if err != nil {
		t.Fatal(err)
	}
	g, _ := body["guardrailConfig"].(map[string]any)
	if g["guardrailIdentifier"] != "gr1" || g["guardrailVersion"] != "3" || g["trace"] != "enabled" {
		t.Fatalf("guardrailConfig = %+v", body["guardrailConfig"])
	}
	if res.Stop != StopGuardrail || !strings.Contains(res.Text, "Sorry") {
		t.Fatalf("stop=%q text=%q", res.Stop, res.Text)
	}
}

func TestGuardrailDropsToolCalls(t *testing.T) {
	a := newAgent(t, Config{
		Model: Mock(Turn{Text: "blocked", Stop: StopGuardrail, ToolCalls: []ToolCall{{ID: "c", Name: "echo", Args: map[string]any{"text": "x"}}}}),
		Tools: []Tool{echoTool("echo", nil)},
	})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopGuardrail || len(res.Steps[0].ToolResults) != 0 {
		t.Fatalf("stop=%q results=%v", res.Stop, res.Steps[0].ToolResults)
	}
}

func TestGuardrailRefusedOutsideBedrock(t *testing.T) {
	g := &Guardrail{ID: "x", Version: "1"}
	for name, m := range map[string]Model{
		"openai":                  OpenAI("m", ModelConfig{Guardrail: g}),
		"anthropic":               Anthropic("m", ModelConfig{Guardrail: g}),
		"gemini":                  Gemini("m", ModelConfig{Guardrail: g}),
		"responses":               OpenAIResponses("m", ModelConfig{Guardrail: g}),
		"groq":                    Groq("m", ModelConfig{Guardrail: g}),
		"bedrock without version": Bedrock("m", ModelConfig{Guardrail: &Guardrail{ID: "x"}}),
	} {
		if _, err := New(Config{Model: m}); err == nil || !strings.Contains(err.Error(), "uardrail") {
			t.Errorf("%s: New err = %v", name, err)
		}
	}
	if _, err := New(Config{Model: Mock(), Fallback: []Model{OpenAI("m", ModelConfig{Guardrail: g})}}); err == nil {
		t.Error("fallback with a guardrail accepted")
	}
}

func TestProviderSafetyStops(t *testing.T) {
	for in, want := range map[string]string{"content_filter": StopGuardrail, "refusal": StopGuardrail, "length": StopLength} {
		if got := stopReason(in); got != want {
			t.Errorf("stopReason(%q) = %q, want %q", in, got, want)
		}
	}
}
