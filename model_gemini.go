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

// Gemini returns a Google Gemini model. The key comes from cfg.APIKey, then
// GEMINI_API_KEY.
func Gemini(model string, cfg ...ModelConfig) Model {
	c := first(cfg)
	if c.BaseURL == "" {
		c.BaseURL = "https://generativelanguage.googleapis.com"
	}
	if c.APIKey == "" {
		c.APIKey = os.Getenv("GEMINI_API_KEY")
	}
	return &geminiModel{name: model, cfg: c}
}

type geminiModel struct {
	name string
	cfg  ModelConfig
}

func (m *geminiModel) Name() string { return "gemini:" + m.name }

type gemPart struct {
	Text         string       `json:"text,omitempty"`
	InlineData   *gemInline   `json:"inlineData,omitempty"`
	FunctionCall *gemFuncCall `json:"functionCall,omitempty"`
	FunctionResp *gemFuncResp `json:"functionResponse,omitempty"`
}

type gemInline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gemFuncCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type gemFuncResp struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type gemContent struct {
	Role  string    `json:"role,omitempty"`
	Parts []gemPart `json:"parts"`
}

type gemRequest struct {
	SystemInstruction *gemContent  `json:"systemInstruction,omitempty"`
	Contents          []gemContent `json:"contents"`
	Tools             []struct {
		FunctionDeclarations []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters,omitempty"`
		} `json:"functionDeclarations"`
	} `json:"tools,omitempty"`
	ToolConfig *struct {
		FunctionCallingConfig struct {
			Mode string `json:"mode"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig,omitempty"`
	GenerationConfig *struct {
		Temperature     *float64       `json:"temperature,omitempty"`
		MaxOutputTokens int            `json:"maxOutputTokens,omitempty"`
		ResponseMime    string         `json:"responseMimeType,omitempty"`
		ResponseSchema  map[string]any `json:"responseSchema,omitempty"`
	} `json:"generationConfig,omitempty"`
}

func (m *geminiModel) Generate(ctx context.Context, req Request) (*Response, error) {
	body := m.request(req)
	b, _ := json.Marshal(body)
	u := strings.TrimSuffix(m.cfg.BaseURL, "/") + "/v1beta/models/" + m.name + ":generateContent?key=" + m.cfg.APIKey
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
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
	var raw gemResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return m.parse([]gemResponse{raw}, req, nil)
}

func (m *geminiModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	if emit == nil {
		emit = func(Delta) {}
	}
	body := m.request(req)
	b, _ := json.Marshal(body)
	u := strings.TrimSuffix(m.cfg.BaseURL, "/") + "/v1beta/models/" + m.name + ":streamGenerateContent?alt=sse&key=" + m.cfg.APIKey
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
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
	var chunks []gemResponse
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var chunk gemResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		chunks = append(chunks, chunk)
		for _, c := range chunk.Candidates {
			for _, p := range c.Content.Parts {
				if p.Text != "" {
					emit(Delta{Text: p.Text})
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return m.parse(chunks, req, nil)
}

type gemResponse struct {
	Candidates []struct {
		Content      gemContent `json:"content"`
		FinishReason string     `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		Prompt int `json:"promptTokenCount"`
		Out    int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

func (m *geminiModel) parse(chunks []gemResponse, req Request, _ func(Delta)) (*Response, error) {
	out := &Response{Stop: "stop"}
	for _, c := range chunks {
		out.Usage.In = c.UsageMetadata.Prompt
		out.Usage.Out = c.UsageMetadata.Out
		for _, cand := range c.Candidates {
			for _, p := range cand.Content.Parts {
				if p.Text != "" {
					out.Text += p.Text
				}
				if p.FunctionCall != nil {
					out.ToolCalls = append(out.ToolCalls, ToolCall{
						ID:   "call_" + p.FunctionCall.Name + "_" + newID()[:6],
						Name: p.FunctionCall.Name, Args: p.FunctionCall.Args})
				}
			}
			if cand.FinishReason != "" {
				switch cand.FinishReason {
				case "MAX_TOKENS":
					out.Stop = "length"
				case "STOP":
					if len(out.ToolCalls) > 0 {
						out.Stop = "tool_calls"
					} else {
						out.Stop = "stop"
					}
				}
			}
		}
	}
	if req.Schema != nil {
		out.Stop = "stop"
	}
	return out, nil
}

func (m *geminiModel) request(req Request) gemRequest {
	out := gemRequest{}
	if req.System != "" {
		out.SystemInstruction = &gemContent{Parts: []gemPart{{Text: req.System}}}
	}
	for _, msg := range req.Messages {
		out.Contents = appendGem(out.Contents, msg)
	}
	if len(req.Tools) > 0 {
		var decls []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters,omitempty"`
		}
		for _, t := range req.Tools {
			decls = append(decls, struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				Parameters  map[string]any `json:"parameters,omitempty"`
			}{t.Name, t.Description, geminiSchema(t.Schema)})
		}
		out.Tools = append(out.Tools, struct {
			FunctionDeclarations []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				Parameters  map[string]any `json:"parameters,omitempty"`
			} `json:"functionDeclarations"`
		}{decls})
	}
	tc := &struct {
		FunctionCallingConfig struct {
			Mode string `json:"mode"`
		} `json:"functionCallingConfig"`
	}{}
	switch req.ToolChoice {
	case "none":
		tc.FunctionCallingConfig.Mode = "NONE"
	case "auto":
		tc.FunctionCallingConfig.Mode = "AUTO"
	}
	if req.Schema != nil {
		tc.FunctionCallingConfig.Mode = "NONE"
		out.GenerationConfig = &struct {
			Temperature     *float64       `json:"temperature,omitempty"`
			MaxOutputTokens int            `json:"maxOutputTokens,omitempty"`
			ResponseMime    string         `json:"responseMimeType,omitempty"`
			ResponseSchema  map[string]any `json:"responseSchema,omitempty"`
		}{Temperature: req.Temperature, MaxOutputTokens: req.MaxTokens,
			ResponseMime: "application/json", ResponseSchema: geminiSchema(req.Schema)}
	} else if req.Temperature != nil || req.MaxTokens > 0 {
		out.GenerationConfig = &struct {
			Temperature     *float64       `json:"temperature,omitempty"`
			MaxOutputTokens int            `json:"maxOutputTokens,omitempty"`
			ResponseMime    string         `json:"responseMimeType,omitempty"`
			ResponseSchema  map[string]any `json:"responseSchema,omitempty"`
		}{Temperature: req.Temperature, MaxOutputTokens: req.MaxTokens}
	}
	if tc.FunctionCallingConfig.Mode != "" {
		out.ToolConfig = tc
	}
	return out
}

func appendGem(contents []gemContent, msg Message) []gemContent {
	add := func(role string, parts []gemPart) []gemContent {
		if n := len(contents); n > 0 && contents[n-1].Role == role {
			contents[n-1].Parts = append(contents[n-1].Parts, parts...)
			return contents
		}
		return append(contents, gemContent{Role: role, Parts: parts})
	}
	switch msg.Role {
	case RoleTool:
		name := msg.ToolName
		if name == "" {
			name = msg.ToolCallID
		}
		return add("user", []gemPart{{FunctionResp: &gemFuncResp{
			Name: name, Response: map[string]any{"result": msg.Content}}}})
	case RoleAssistant:
		var parts []gemPart
		if msg.Content != "" {
			parts = append(parts, gemPart{Text: msg.Content})
		}
		for _, c := range msg.ToolCalls {
			parts = append(parts, gemPart{FunctionCall: &gemFuncCall{Name: c.Name, Args: c.Args}})
		}
		return add("model", parts)
	default:
		var parts []gemPart
		if msg.Content != "" {
			parts = append(parts, gemPart{Text: msg.Content})
		}
		for _, img := range msg.Images {
			parts = append(parts, gemPart{InlineData: &gemInline{MimeType: img.MediaType,
				Data: base64.StdEncoding.EncodeToString(img.Data)}})
		}
		return add("user", parts)
	}
}

// geminiSchema converts our lowercase JSON Schema into the subset Gemini
// accepts, with uppercase type names.
func geminiSchema(s map[string]any) map[string]any {
	if s == nil {
		return nil
	}
	out := map[string]any{}
	for k, v := range s {
		switch k {
		case "type":
			if t, ok := v.(string); ok {
				out["type"] = strings.ToUpper(t)
			}
		case "properties":
			if props, ok := v.(map[string]any); ok {
				m := map[string]any{}
				for name, raw := range props {
					if pm, ok := raw.(map[string]any); ok {
						m[name] = geminiSchema(pm)
					}
				}
				out["properties"] = m
			}
		case "items":
			if im, ok := v.(map[string]any); ok {
				out["items"] = geminiSchema(im)
			}
		case "required", "enum", "description":
			out[k] = v
		}
	}
	return out
}
