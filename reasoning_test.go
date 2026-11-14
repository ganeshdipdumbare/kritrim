package kritrim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReasoningValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    *Reasoning
		ok   bool
	}{
		{"nil", nil, true},
		{"effort", &Reasoning{Effort: "high"}, true},
		{"budget", &Reasoning{Budget: 2048}, true},
		{"both", &Reasoning{Effort: "low", Budget: 8192}, true},
		{"bad effort", &Reasoning{Effort: "extreme"}, false},
		{"empty", &Reasoning{}, false},
		{"tiny budget", &Reasoning{Budget: 10}, false},
		{"negative", &Reasoning{Budget: -1}, false},
	} {
		err := tc.r.validate()
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v, want ok=%v", tc.name, err, tc.ok)
		}
	}
	if _, err := New(Config{Model: Mock(), Reasoning: &Reasoning{Effort: "extreme"}}); err == nil {
		t.Fatal("New accepted a bad effort")
	}
}

func TestReasoningBudgetAndEffort(t *testing.T) {
	cases := []struct {
		r      *Reasoning
		budget int
		effort string
	}{
		{nil, 0, ""},
		{&Reasoning{Effort: "low"}, budgetLow, "low"},
		{&Reasoning{Effort: "medium"}, budgetMedium, "medium"},
		{&Reasoning{Effort: "high"}, budgetHigh, "high"},
		{&Reasoning{Budget: 1024}, 1024, "low"},
		{&Reasoning{Budget: 5000}, 5000, "medium"},
		{&Reasoning{Budget: 40000}, 40000, "high"},
		// An explicit budget wins over the effort it would imply.
		{&Reasoning{Effort: "low", Budget: 20000}, 20000, "low"},
	}
	for _, tc := range cases {
		if got := tc.r.budget(); got != tc.budget {
			t.Errorf("%+v budget=%d want %d", tc.r, got, tc.budget)
		}
		if got := tc.r.effort(); got != tc.effort {
			t.Errorf("%+v effort=%q want %q", tc.r, got, tc.effort)
		}
	}
}

// TestReasoningReachesEveryProvider checks the on-the-wire mapping for each
// provider that supports reasoning.
func TestReasoningReachesEveryProvider(t *testing.T) {
	temp := 0.7
	req := Request{
		Messages:    []Message{{Role: RoleUser, Content: "hi"}},
		Temperature: &temp,
		MaxTokens:   512,
		Reasoning:   &Reasoning{Effort: "high", Include: true},
	}

	t.Run("anthropic", func(t *testing.T) {
		body := captureJSON(t, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
			func(url string) error {
				_, err := Anthropic("claude-x", ModelConfig{BaseURL: url, APIKey: "k"}).Generate(context.Background(), req)
				return err
			})
		think, _ := body["thinking"].(map[string]any)
		if think == nil || think["type"] != "enabled" || think["budget_tokens"].(float64) != budgetHigh {
			t.Fatalf("thinking=%v", body["thinking"])
		}
		// Room to think beyond the budget, and no temperature.
		if body["max_tokens"].(float64) <= budgetHigh {
			t.Fatalf("max_tokens %v does not clear the budget", body["max_tokens"])
		}
		if _, ok := body["temperature"]; ok {
			t.Fatal("temperature sent with thinking enabled")
		}
	})

	t.Run("openai", func(t *testing.T) {
		body := captureJSON(t, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
			func(url string) error {
				_, err := OpenAI("o-x", ModelConfig{BaseURL: url, APIKey: "k"}).Generate(context.Background(), req)
				return err
			})
		if body["reasoning_effort"] != "high" {
			t.Fatalf("reasoning_effort=%v", body["reasoning_effort"])
		}
		if _, ok := body["temperature"]; ok {
			t.Fatal("temperature sent to a reasoning model")
		}
	})

	t.Run("openai responses", func(t *testing.T) {
		body := captureJSON(t, `{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`,
			func(url string) error {
				_, err := OpenAIResponses("o-x", ModelConfig{BaseURL: url, APIKey: "k"}).Generate(context.Background(), req)
				return err
			})
		r, _ := body["reasoning"].(map[string]any)
		if r == nil || r["effort"] != "high" || r["summary"] != "auto" {
			t.Fatalf("reasoning=%v", body["reasoning"])
		}
	})

	t.Run("gemini", func(t *testing.T) {
		body := captureJSON(t, `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{}}`,
			func(url string) error {
				_, err := Gemini("gemini-x", ModelConfig{BaseURL: url, APIKey: "k"}).Generate(context.Background(), req)
				return err
			})
		gen, _ := body["generationConfig"].(map[string]any)
		think, _ := gen["thinkingConfig"].(map[string]any)
		if think == nil || think["thinkingBudget"].(float64) != budgetHigh || think["includeThoughts"] != true {
			t.Fatalf("thinkingConfig=%v", gen)
		}
	})

	t.Run("bedrock", func(t *testing.T) {
		body := captureJSON(t, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{}}`,
			func(url string) error {
				m := Bedrock("anthropic.claude-x", ModelConfig{BaseURL: url, Region: "us-east-1",
					AccessKey: "AK", SecretKey: "SK"})
				_, err := m.Generate(context.Background(), req)
				return err
			})
		extra, _ := body["additionalModelRequestFields"].(map[string]any)
		rc, _ := extra["reasoning_config"].(map[string]any)
		if rc == nil || rc["type"] != "enabled" || rc["budget_tokens"].(float64) != budgetHigh {
			t.Fatalf("additionalModelRequestFields=%v", extra)
		}
		if _, ok := body["inferenceConfig"].(map[string]any)["temperature"]; ok {
			t.Fatal("temperature sent with reasoning enabled")
		}
	})
}

// captureJSON serves reply for one request and returns the decoded body.
func captureJSON(t *testing.T, reply string, call func(url string) error) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if strings.HasPrefix(reply, "data:") {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	if err := call(srv.URL); err != nil {
		t.Fatalf("call: %v", err)
	}
	return body
}

// TestReasoningSignatureIsReplayed covers Anthropic's rule that a tool
// result is refused unless the thinking block that asked for it comes back
// with its signature.
func TestReasoningSignatureIsReplayed(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "refund A-17"},
		{Role: RoleAssistant, Reasoning: "they asked for a refund", ReasoningSig: "sig-abc",
			ToolCalls: []ToolCall{{ID: "c1", Name: "refund", Args: map[string]any{"id": "A-17"}}}},
		{Role: RoleTool, ToolCallID: "c1", ToolName: "refund", Content: "done"},
	}
	var out []anMsg
	for _, m := range msgs {
		out = appendAn(out, m)
	}
	if len(out) != 3 {
		t.Fatalf("got %d messages: %+v", len(out), out)
	}
	first := out[1].Content[0]
	if first.Type != "thinking" || first.Thinking != "they asked for a refund" || first.Signature != "sig-abc" {
		t.Fatalf("assistant blocks = %+v", out[1].Content)
	}
}

// TestReasoningCountsAgainstTheBudget covers the pre-call check: thinking
// tokens are output tokens, so a budget that cannot hold them is refused
// instead of being silently capped below the thinking budget.
func TestReasoningCountsAgainstTheBudget(t *testing.T) {
	req := Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}, Reasoning: &Reasoning{Effort: "high"}}
	if err := fitBudget(&req, 0, 2000); err == nil {
		t.Fatal("a 2000-token budget accepted a 16384-token thinking budget")
	}
	req2 := Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}, Reasoning: &Reasoning{Effort: "low"}}
	if err := fitBudget(&req2, 0, 100_000); err != nil {
		t.Fatalf("a 100k budget refused a 1024-token thinking budget: %v", err)
	}
}

// TestReasoningRecordedOnTheAssistantMessage checks the loop keeps the
// reasoning on the message it belongs to, so it survives a session round
// trip.
func TestReasoningRecordedOnTheAssistantMessage(t *testing.T) {
	a, err := New(Config{Model: &reasoningModel{}, Session: MemorySessions()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	res, err := a.Run(context.Background(), "hi", Thread("t1"))
	if err != nil {
		t.Fatal(err)
	}
	last := res.Messages[len(res.Messages)-1]
	if last.Reasoning != "thought hard" || last.ReasoningSig != "sig1" {
		t.Fatalf("assistant message = %+v", last)
	}
	sess, err := a.Session(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got := sess.Messages[len(sess.Messages)-1].ReasoningSig; got != "sig1" {
		t.Fatalf("stored signature = %q", got)
	}
}

type reasoningModel struct{}

func (reasoningModel) Generate(ctx context.Context, req Request) (*Response, error) {
	return &Response{Text: "done", Reasoning: "thought hard", ReasoningSig: "sig1", Stop: "stop"}, nil
}

func (m reasoningModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	return m.Generate(ctx, req)
}
