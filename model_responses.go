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

// OpenAIResponses returns an OpenAI Responses API model. The key comes from
// cfg.APIKey, then OPENAI_API_KEY.
func OpenAIResponses(model string, cfg ...ModelConfig) Model {
	c := first(cfg)
	if c.BaseURL == "" {
		c.BaseURL = "https://api.openai.com/v1"
	}
	if c.APIKey == "" {
		c.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	return &responsesModel{name: model, cfg: c}
}

type responsesModel struct {
	name string
	cfg  ModelConfig
}

func (m *responsesModel) Name() string { return "openai-responses:" + m.name }

type rPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type rInput struct {
	Type      string  `json:"type,omitempty"`
	Role      string  `json:"role,omitempty"`
	Content   []rPart `json:"content,omitempty"`
	CallID    string  `json:"call_id,omitempty"`
	Name      string  `json:"name,omitempty"`
	Arguments string  `json:"arguments,omitempty"`
	Output    string  `json:"output,omitempty"`
}

type rTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type rRequest struct {
	Model           string   `json:"model"`
	Instructions    string   `json:"instructions,omitempty"`
	Input           []rInput `json:"input"`
	Tools           []rTool  `json:"tools,omitempty"`
	ToolChoice      any      `json:"tool_choice,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	Text            *rText   `json:"text,omitempty"`
	Stream          bool     `json:"stream,omitempty"`
}

type rText struct {
	Format *rFormat `json:"format,omitempty"`
}

type rFormat struct {
	Type   string         `json:"type"`
	Name   string         `json:"name,omitempty"`
	Schema map[string]any `json:"schema,omitempty"`
	Strict bool           `json:"strict,omitempty"`
}

type rResponse struct {
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"output"`
	Status string `json:"status"`
	Usage  struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (m *responsesModel) Generate(ctx context.Context, req Request) (*Response, error) {
	return m.do(ctx, req, false, nil)
}

func (m *responsesModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	if emit == nil {
		emit = func(Delta) {}
	}
	return m.do(ctx, req, true, emit)
}

func (m *responsesModel) do(ctx context.Context, req Request, stream bool, emit func(Delta)) (*Response, error) {
	body := m.request(req, stream)
	b, _ := json.Marshal(body)
	u := strings.TrimSuffix(m.cfg.BaseURL, "/") + "/responses"
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
		return nil, &StatusError{Provider: m.Name(), Code: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(msg))}
	}
	if !stream {
		var raw rResponse
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			return nil, err
		}
		return m.parse(raw)
	}
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
		var ev struct {
			Type     string    `json:"type"`
			Delta    string    `json:"delta"`
			Response rResponse `json:"response"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "response.output_text.delta":
			emit(Delta{Text: ev.Delta})
		case "response.completed":
			return m.parse(ev.Response)
		case "response.failed", "error":
			msg := "response failed"
			if ev.Error != nil {
				msg = ev.Error.Message
			}
			return nil, fmt.Errorf("kritrim: %s: %s", m.Name(), msg)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("kritrim: %s: stream ended without response.completed", m.Name())
}

func (m *responsesModel) request(req Request, stream bool) rRequest {
	out := rRequest{Model: m.name, Instructions: req.System, MaxOutputTokens: req.MaxTokens,
		Temperature: req.Temperature, Stream: stream}
	for _, msg := range req.Messages {
		out.Input = appendRInput(out.Input, msg)
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, rTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Schema})
	}
	switch req.ToolChoice {
	case "none":
		out.ToolChoice = "none"
	case "auto":
		out.ToolChoice = "auto"
	}
	if req.Schema != nil {
		name := req.SchemaName
		if name == "" {
			name = "output"
		}
		out.Text = &rText{Format: &rFormat{Type: "json_schema", Name: name, Schema: req.Schema}}
	}
	return out
}

func appendRInput(in []rInput, msg Message) []rInput {
	switch msg.Role {
	case RoleTool:
		return append(in, rInput{Type: "function_call_output", CallID: msg.ToolCallID, Output: msg.Content})
	case RoleAssistant:
		if msg.Content != "" {
			in = append(in, rInput{Role: "assistant", Content: []rPart{{Type: "output_text", Text: msg.Content}}})
		}
		for _, c := range msg.ToolCalls {
			in = append(in, rInput{Type: "function_call", CallID: c.ID, Name: c.Name, Arguments: jsonString(c.Args)})
		}
		return in
	default:
		var parts []rPart
		if msg.Content != "" {
			parts = append(parts, rPart{Type: "input_text", Text: msg.Content})
		}
		for _, img := range msg.Images {
			parts = append(parts, rPart{Type: "input_image",
				ImageURL: "data:" + img.MediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)})
		}
		return append(in, rInput{Role: "user", Content: parts})
	}
}

func (m *responsesModel) parse(raw rResponse) (*Response, error) {
	if raw.Error != nil {
		return nil, fmt.Errorf("kritrim: %s: %s", m.Name(), raw.Error.Message)
	}
	out := &Response{Usage: Usage{In: raw.Usage.InputTokens, Out: raw.Usage.OutputTokens}, Stop: "stop"}
	for _, item := range raw.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" {
					out.Text += c.Text
				}
			}
		case "function_call":
			args := map[string]any{}
			if strings.TrimSpace(item.Arguments) != "" {
				if err := json.Unmarshal([]byte(item.Arguments), &args); err != nil {
					return nil, fmt.Errorf("kritrim: %s: tool %s arguments: %w", m.Name(), item.Name, err)
				}
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: item.CallID, Name: item.Name, Args: args})
		}
	}
	if len(out.ToolCalls) > 0 {
		out.Stop = "tool_calls"
	}
	return out, nil
}
