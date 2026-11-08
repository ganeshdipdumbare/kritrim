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

// Anthropic returns an Anthropic messages model. The key comes from
// cfg.APIKey, then ANTHROPIC_API_KEY.
func Anthropic(model string, cfg ...ModelConfig) Model {
	c := first(cfg)
	if c.BaseURL == "" {
		c.BaseURL = "https://api.anthropic.com"
	}
	if c.APIKey == "" {
		c.APIKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	return &anthropicModel{name: model, cfg: c}
}

type anthropicModel struct {
	name string
	cfg  ModelConfig
}

func (m *anthropicModel) Name() string { return "anthropic:" + m.name }

type anBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   any            `json:"content,omitempty"`
	Source    any            `json:"source,omitempty"`
}

type anMsg struct {
	Role    string    `json:"role"`
	Content []anBlock `json:"content"`
}

type anRequest struct {
	Model       string   `json:"model"`
	MaxTokens   int      `json:"max_tokens"`
	System      string   `json:"system,omitempty"`
	Messages    []anMsg  `json:"messages"`
	Tools       []anTool `json:"tools,omitempty"`
	ToolChoice  any      `json:"tool_choice,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	Stream      bool     `json:"stream,omitempty"`
}

type anTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

func (m *anthropicModel) Generate(ctx context.Context, req Request) (*Response, error) {
	return m.do(ctx, req, false, nil)
}

func (m *anthropicModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	if emit == nil {
		emit = func(Delta) {}
	}
	return m.do(ctx, req, true, emit)
}

func (m *anthropicModel) do(ctx context.Context, req Request, stream bool, emit func(Delta)) (*Response, error) {
	body := m.request(req, stream)
	b, _ := json.Marshal(body)
	u := strings.TrimSuffix(m.cfg.BaseURL, "/") + "/v1/messages"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("anthropic-version", "2023-06-01")
	if m.cfg.APIKey != "" {
		hreq.Header.Set("x-api-key", m.cfg.APIKey)
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
		var raw struct {
			Content []anBlock `json:"content"`
			Usage   struct {
				Input  int `json:"input_tokens"`
				Output int `json:"output_tokens"`
			} `json:"usage"`
			StopReason string `json:"stop_reason"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			return nil, err
		}
		out := &Response{Usage: Usage{In: raw.Usage.Input, Out: raw.Usage.Output}, Stop: stopReason(raw.StopReason)}
		m.collect(out, raw.Content, nil, req)
		return out, nil
	}
	return m.readSSE(ctx, resp.Body, req, emit)
}

func (m *anthropicModel) request(req Request, stream bool) anRequest {
	max := req.MaxTokens
	if max == 0 {
		max = 4096
	}
	out := anRequest{Model: m.name, MaxTokens: max, System: req.System, Temperature: req.Temperature, Stream: stream}
	for _, msg := range req.Messages {
		out.Messages = appendAn(out.Messages, msg)
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, anTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	switch req.ToolChoice {
	case "none":
		out.ToolChoice = map[string]any{"type": "none"}
	case "auto":
		out.ToolChoice = map[string]any{"type": "auto"}
	}
	if req.Schema != nil {
		name := req.SchemaName
		if name == "" {
			name = "output"
		}
		out.Tools = append(out.Tools, anTool{Name: name, Description: "Return the result", InputSchema: req.Schema})
		out.ToolChoice = map[string]any{"type": "tool", "name": name}
	}
	return out
}

func appendAn(msgs []anMsg, msg Message) []anMsg {
	switch msg.Role {
	case RoleTool:
		block := anBlock{Type: "tool_result", ToolUseID: msg.ToolCallID, Content: msg.Content}
		if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
			msgs[n-1].Content = append(msgs[n-1].Content, block)
			return msgs
		}
		return append(msgs, anMsg{Role: "user", Content: []anBlock{block}})
	case RoleAssistant:
		var blocks []anBlock
		if msg.Content != "" {
			blocks = append(blocks, anBlock{Type: "text", Text: msg.Content})
		}
		for _, c := range msg.ToolCalls {
			blocks = append(blocks, anBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: c.Args})
		}
		return append(msgs, anMsg{Role: "assistant", Content: blocks})
	default:
		var blocks []anBlock
		if msg.Content != "" {
			blocks = append(blocks, anBlock{Type: "text", Text: msg.Content})
		}
		for _, img := range msg.Images {
			blocks = append(blocks, anBlock{Type: "image", Source: map[string]any{
				"type": "base64", "media_type": img.MediaType,
				"data": base64.StdEncoding.EncodeToString(img.Data)}})
		}
		if len(blocks) == 0 {
			blocks = append(blocks, anBlock{Type: "text", Text: ""})
		}
		return append(msgs, anMsg{Role: "user", Content: blocks})
	}
}

func (m *anthropicModel) readSSE(ctx context.Context, r io.Reader, req Request, emit func(Delta)) (*Response, error) {
	out := &Response{Stop: "stop"}
	type block struct {
		typ  string
		name string
		id   string
		json strings.Builder
	}
	blocks := map[int]*block{}
	var text strings.Builder
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Usage struct {
					Input int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock anBlock `json:"content_block"`
			Delta        struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				Output int `json:"output_tokens"`
				Input  int `json:"input_tokens"`
			} `json:"usage"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "error":
			return nil, fmt.Errorf("kritrim: %s: %s", m.Name(), ev.Error.Message)
		case "message_start":
			out.Usage.In = ev.Message.Usage.Input
		case "content_block_start":
			b := &block{typ: ev.ContentBlock.Type, name: ev.ContentBlock.Name, id: ev.ContentBlock.ID}
			blocks[ev.Index] = b
		case "content_block_delta":
			if b := blocks[ev.Index]; b != nil {
				switch ev.Delta.Type {
				case "text_delta":
					text.WriteString(ev.Delta.Text)
					emit(Delta{Text: ev.Delta.Text})
				case "thinking_delta":
					emit(Delta{Reasoning: ev.Delta.Thinking})
					out.Reasoning += ev.Delta.Thinking
				case "input_json_delta":
					b.json.WriteString(ev.Delta.PartialJSON)
				}
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				out.Stop = stopReason(ev.Delta.StopReason)
			}
			if ev.Usage.Output > 0 {
				out.Usage.Out = ev.Usage.Output
			}
			if ev.Usage.Input > 0 {
				out.Usage.In = ev.Usage.Input
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out.Text = text.String()
	for i := 0; i < len(blocks); i++ {
		b := blocks[i]
		if b == nil || b.typ != "tool_use" {
			continue
		}
		input := map[string]any{}
		if s := strings.TrimSpace(b.json.String()); s != "" {
			if err := json.Unmarshal([]byte(s), &input); err != nil {
				return nil, fmt.Errorf("kritrim: %s: tool %s input: %w", m.Name(), b.name, err)
			}
		}
		if req.Schema != nil && b.name == schemaTool(req) {
			out.Text = jsonString(input)
			continue
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.id, Name: b.name, Args: input})
	}
	return out, nil
}

func (m *anthropicModel) collect(out *Response, blocks []anBlock, _ map[string]any, req Request) {
	var text strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "thinking":
			out.Reasoning += b.Text
		case "tool_use":
			if req.Schema != nil && b.Name == schemaTool(req) {
				out.Text = jsonString(b.Input)
				continue
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: b.Input})
		}
	}
	out.Text = text.String()
}

func schemaTool(req Request) string {
	if req.SchemaName != "" {
		return req.SchemaName
	}
	return "output"
}
