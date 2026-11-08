package kritrim

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// OpenAI returns an OpenAI chat-completions model. The key comes from
// cfg.APIKey, then OPENAI_API_KEY.
func OpenAI(model string, cfg ...ModelConfig) Model {
	c := first(cfg)
	if c.BaseURL == "" {
		c.BaseURL = "https://api.openai.com/v1"
	}
	if c.APIKey == "" {
		c.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	return &openaiModel{name: model, cfg: c}
}

// Ollama returns a model served by a local Ollama instance.
func Ollama(model string, cfg ...ModelConfig) Model {
	c := first(cfg)
	if c.BaseURL == "" {
		c.BaseURL = "http://localhost:11434/v1"
	}
	if c.APIKey == "" {
		c.APIKey = "ollama"
	}
	if c.JSONMode == "" {
		c.JSONMode = "json_schema"
	}
	return &openaiModel{name: model, cfg: c}
}

// Compatible returns any OpenAI-compatible provider, such as DeepSeek,
// Groq, OpenRouter or vLLM.
func Compatible(provider, baseURL, model string, cfg ...ModelConfig) Model {
	c := first(cfg)
	c.BaseURL = strings.TrimSuffix(baseURL, "/")
	if c.APIKey == "" {
		c.APIKey = os.Getenv(strings.ToUpper(provider) + "_API_KEY")
	}
	switch strings.ToLower(provider) {
	case "deepseek", "groq":
		if c.JSONMode == "" {
			c.JSONMode = "json_object"
		}
	}
	if c.JSONMode == "" {
		c.JSONMode = "json_schema"
	}
	return &openaiModel{name: model, cfg: c}
}

type openaiModel struct {
	name string
	cfg  ModelConfig
}

func (m *openaiModel) Name() string { return "openai:" + m.name }

type oaMsg struct {
	Role       string       `json:"role"`
	Content    any          `json:"content,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model          string   `json:"model"`
	Messages       []oaMsg  `json:"messages"`
	Tools          []oaTool `json:"tools,omitempty"`
	ToolChoice     any      `json:"tool_choice,omitempty"`
	Temperature    *float64 `json:"temperature,omitempty"`
	MaxTokens      int      `json:"max_tokens,omitempty"`
	ResponseFormat any      `json:"response_format,omitempty"`
	Stream         bool     `json:"stream,omitempty"`
	StreamOptions  any      `json:"stream_options,omitempty"`
}

func (m *openaiModel) Generate(ctx context.Context, req Request) (*Response, error) {
	return m.Stream(ctx, req, nil)
}

func (m *openaiModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	if emit == nil {
		emit = func(Delta) {}
	}
	body := m.request(req, true)
	b, _ := json.Marshal(body)
	u := strings.TrimSuffix(m.cfg.BaseURL, "/") + "/chat/completions"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if m.cfg.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+m.cfg.APIKey)
	}
	for k, v := range m.cfg.Headers {
		hreq.Header.Set(k, v)
	}
	resp, err := m.cfg.client().Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("kritrim: %s: %s: %s", m.Name(), resp.Status, strings.TrimSpace(string(msg)))
	}

	var out Response
	type acc struct {
		id, name string
		args     string
	}
	calls := map[int]*acc{}
	var order []int
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			out.Usage = Usage{In: chunk.Usage.PromptTokens, Out: chunk.Usage.CompletionTokens}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		c := chunk.Choices[0]
		if c.Delta.Content != "" {
			out.Text += c.Delta.Content
			emit(Delta{Text: c.Delta.Content})
		}
		if c.Delta.Reasoning != "" {
			out.Reasoning += c.Delta.Reasoning
			emit(Delta{Reasoning: c.Delta.Reasoning})
		}
		for _, tc := range c.Delta.ToolCalls {
			a := calls[tc.Index]
			if a == nil {
				a = &acc{}
				calls[tc.Index] = a
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				a.id = tc.ID
			}
			if tc.Function.Name != "" {
				a.name += tc.Function.Name
			}
			a.args += tc.Function.Arguments
		}
		if c.FinishReason != "" {
			out.Stop = stopReason(c.FinishReason)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for _, i := range order {
		a := calls[i]
		if a.id == "" {
			a.id = "call_" + a.name + "_" + newID()[:6]
		}
		args := map[string]any{}
		if strings.TrimSpace(a.args) != "" {
			if err := json.Unmarshal([]byte(a.args), &args); err != nil {
				return nil, fmt.Errorf("kritrim: %s: tool %s arguments: %w", m.Name(), a.name, err)
			}
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: a.id, Name: a.name, Args: args})
	}
	if out.Stop == "" {
		if len(out.ToolCalls) > 0 {
			out.Stop = "tool_calls"
		} else {
			out.Stop = "stop"
		}
	}
	return &out, nil
}

func stopReason(s string) string {
	switch s {
	case "tool_calls", "tool_use", "function_call":
		return "tool_calls"
	case "length", "max_tokens":
		return "length"
	default:
		return "stop"
	}
}

func (m *openaiModel) request(req Request, stream bool) oaRequest {
	out := oaRequest{Model: m.name, Temperature: req.Temperature, MaxTokens: req.MaxTokens, Stream: stream}
	if stream {
		out.StreamOptions = map[string]any{"include_usage": true}
	}
	if req.System != "" {
		out.Messages = append(out.Messages, oaMsg{Role: "system", Content: req.System})
	}
	for _, msg := range req.Messages {
		out.Messages = append(out.Messages, toOAMessage(msg))
	}
	for _, t := range req.Tools {
		var x oaTool
		x.Type = "function"
		x.Function.Name = t.Name
		x.Function.Description = t.Description
		x.Function.Parameters = t.Schema
		out.Tools = append(out.Tools, x)
	}
	switch req.ToolChoice {
	case "none":
		out.ToolChoice = "none"
	case "auto":
		out.ToolChoice = "auto"
	}
	if req.Schema != nil {
		if m.cfg.JSONMode == "json_object" {
			out.ResponseFormat = map[string]any{"type": "json_object"}
			out.Messages = append(out.Messages, oaMsg{Role: "system",
				Content: "Reply with only a JSON object that matches this schema: " + jsonString(req.Schema)})
			out.ToolChoice = "none"
		} else {
			out.ResponseFormat = map[string]any{"type": "json_schema",
				"json_schema": map[string]any{"name": req.SchemaName, "schema": req.Schema, "strict": false}}
		}
	}
	return out
}

func toOAMessage(msg Message) oaMsg {
	out := oaMsg{Role: string(msg.Role), ToolCallID: msg.ToolCallID}
	if len(msg.Images) > 0 {
		var parts []any
		if msg.Content != "" {
			parts = append(parts, map[string]any{"type": "text", "text": msg.Content})
		}
		for _, img := range msg.Images {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{
				"url": "data:" + img.MediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)}})
		}
		out.Content = parts
	} else {
		out.Content = msg.Content
	}
	for _, c := range msg.ToolCalls {
		var tc oaToolCall
		tc.ID, tc.Type = c.ID, "function"
		tc.Function.Name = c.Name
		tc.Function.Arguments = jsonString(c.Args)
		out.ToolCalls = append(out.ToolCalls, tc)
	}
	return out
}
