package kritrim

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Bedrock returns an Amazon Bedrock model using the Converse API. Region and
// credentials come from ModelConfig, then the AWS_* environment variables.
// Streaming falls back to one text delta (Converse is non-streaming here).
func Bedrock(model string, cfg ...ModelConfig) Model {
	c := first(cfg)
	if c.Region == "" {
		c.Region = os.Getenv("AWS_REGION")
	}
	if c.Region == "" {
		c.Region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	if c.AccessKey == "" {
		c.AccessKey = os.Getenv("AWS_ACCESS_KEY_ID")
	}
	if c.SecretKey == "" {
		c.SecretKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}
	if c.SessionToken == "" {
		c.SessionToken = os.Getenv("AWS_SESSION_TOKEN")
	}
	return &bedrockModel{name: model, cfg: c}
}

type bedrockModel struct {
	name string
	cfg  ModelConfig
}

func (m *bedrockModel) Name() string { return "bedrock:" + m.name }

func (m *bedrockModel) endpoint() string {
	if m.cfg.BaseURL != "" {
		return strings.TrimSuffix(m.cfg.BaseURL, "/") + "/model/" + m.name + "/converse"
	}
	return "https://bedrock-runtime." + m.cfg.Region + ".amazonaws.com/model/" + m.name + "/converse"
}

func (m *bedrockModel) Generate(ctx context.Context, req Request) (*Response, error) {
	body, err := json.Marshal(m.request(req))
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if err := signAWS(hreq, body, "bedrock", m.cfg.Region, m.cfg.AccessKey, m.cfg.SecretKey, m.cfg.SessionToken); err != nil {
		return nil, err
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
	var raw brResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return m.parse(raw, req), nil
}

func (m *bedrockModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	resp, err := m.Generate(ctx, req)
	if err == nil && resp.Text != "" && emit != nil {
		emit(Delta{Text: resp.Text})
	}
	return resp, err
}

type brContent struct {
	Text       string        `json:"text,omitempty"`
	ToolUse    *brToolUse    `json:"toolUse,omitempty"`
	ToolResult *brToolResult `json:"toolResult,omitempty"`
}

type brToolUse struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input,omitempty"`
}

type brToolResult struct {
	ToolUseID string      `json:"toolUseId"`
	Content   []brContent `json:"content"`
	Status    string      `json:"status,omitempty"`
}

type brMessage struct {
	Role    string      `json:"role"`
	Content []brContent `json:"content"`
}

type brToolSpec struct {
	ToolSpec struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		InputSchema struct {
			JSON map[string]any `json:"json"`
		} `json:"inputSchema"`
	} `json:"toolSpec"`
}

type brRequest struct {
	Messages        []brMessage `json:"messages"`
	System          []brContent `json:"system,omitempty"`
	InferenceConfig *struct {
		MaxTokens   int      `json:"maxTokens,omitempty"`
		Temperature *float64 `json:"temperature,omitempty"`
	} `json:"inferenceConfig,omitempty"`
	ToolConfig *struct {
		Tools      []brToolSpec `json:"tools"`
		ToolChoice any          `json:"toolChoice,omitempty"`
	} `json:"toolConfig,omitempty"`
}

type brResponse struct {
	Output struct {
		Message brMessage `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"usage"`
}

func (m *bedrockModel) request(req Request) brRequest {
	out := brRequest{}
	if req.System != "" {
		out.System = []brContent{{Text: req.System}}
	}
	for _, msg := range req.Messages {
		out.Messages = appendBR(out.Messages, msg)
	}
	if req.MaxTokens > 0 || req.Temperature != nil {
		out.InferenceConfig = &struct {
			MaxTokens   int      `json:"maxTokens,omitempty"`
			Temperature *float64 `json:"temperature,omitempty"`
		}{req.MaxTokens, req.Temperature}
	}
	if len(req.Tools) > 0 && req.ToolChoice != "none" {
		tc := &struct {
			Tools      []brToolSpec `json:"tools"`
			ToolChoice any          `json:"toolChoice,omitempty"`
		}{}
		for _, t := range req.Tools {
			var spec brToolSpec
			spec.ToolSpec.Name = t.Name
			spec.ToolSpec.Description = t.Description
			spec.ToolSpec.InputSchema.JSON = t.Schema
			tc.Tools = append(tc.Tools, spec)
		}
		switch req.ToolChoice {
		case "auto":
			tc.ToolChoice = map[string]any{"auto": map[string]any{}}
		}
		if req.Schema != nil {
			name := req.SchemaName
			if name == "" {
				name = "output"
			}
			var spec brToolSpec
			spec.ToolSpec.Name = name
			spec.ToolSpec.Description = "Return the result"
			spec.ToolSpec.InputSchema.JSON = req.Schema
			tc.Tools = append(tc.Tools, spec)
			tc.ToolChoice = map[string]any{"tool": map[string]any{"name": name}}
		}
		out.ToolConfig = tc
	}
	return out
}

func appendBR(msgs []brMessage, msg Message) []brMessage {
	switch msg.Role {
	case RoleTool:
		block := brContent{ToolResult: &brToolResult{ToolUseID: msg.ToolCallID, Content: []brContent{{Text: msg.Content}}}}
		if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
			msgs[n-1].Content = append(msgs[n-1].Content, block)
			return msgs
		}
		return append(msgs, brMessage{Role: "user", Content: []brContent{block}})
	case RoleAssistant:
		var blocks []brContent
		if msg.Content != "" {
			blocks = append(blocks, brContent{Text: msg.Content})
		}
		for _, c := range msg.ToolCalls {
			blocks = append(blocks, brContent{ToolUse: &brToolUse{ToolUseID: c.ID, Name: c.Name, Input: c.Args}})
		}
		return append(msgs, brMessage{Role: "assistant", Content: blocks})
	default:
		var blocks []brContent
		if msg.Content != "" {
			blocks = append(blocks, brContent{Text: msg.Content})
		}
		return append(msgs, brMessage{Role: "user", Content: blocks})
	}
}

func (m *bedrockModel) parse(raw brResponse, req Request) *Response {
	out := &Response{Usage: Usage{In: raw.Usage.InputTokens, Out: raw.Usage.OutputTokens}}
	for _, b := range raw.Output.Message.Content {
		switch {
		case b.ToolUse != nil:
			if req.Schema != nil && b.ToolUse.Name == schemaTool(req) {
				out.Text = jsonString(b.ToolUse.Input)
				continue
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.ToolUse.ToolUseID, Name: b.ToolUse.Name, Args: b.ToolUse.Input})
		default:
			out.Text += b.Text
		}
	}
	switch raw.StopReason {
	case "tool_use":
		out.Stop = "tool_calls"
	case "max_tokens":
		out.Stop = "length"
	default:
		out.Stop = "stop"
	}
	return out
}

// signAWS adds a SigV4 Authorization header.
func signAWS(req *http.Request, body []byte, service, region, access, secret, token string) error {
	if access == "" || secret == "" {
		return fmt.Errorf("kritrim: AWS credentials are not set")
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	payloadHash := sha256Hex(body)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if token != "" {
		req.Header.Set("X-Amz-Security-Token", token)
	}
	headers := map[string]string{
		"content-type":         req.Header.Get("Content-Type"),
		"host":                 req.URL.Host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	if token != "" {
		headers["x-amz-security-token"] = token
	}
	var keys []string
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var canonicalHeaders strings.Builder
	for _, k := range keys {
		canonicalHeaders.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
	}
	signed := strings.Join(keys, ";")
	canonical := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.Query().Encode(),
		canonicalHeaders.String(),
		signed,
		payloadHash,
	}, "\n")
	scope := date + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	key := hmacSHA256([]byte("AWS4"+secret), date)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+access+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, s string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(s))
	return mac.Sum(nil)
}
