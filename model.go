package kritrim

import (
	"context"
	"net/http"
	"time"
)

// Model is one model provider bound to a model name.
type Model interface {
	// Generate runs one model call to completion.
	Generate(ctx context.Context, req Request) (*Response, error)
	// Stream runs one model call, calling emit for each text or reasoning
	// delta, and returns the same response Generate would.
	Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error)
}

// Request is one model call.
type Request struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []Tool
	MaxTokens   int
	Temperature *float64
	ToolChoice  string // "", "auto" or "none"
	Schema      map[string]any
	SchemaName  string
}

// Delta is a partial model output.
type Delta struct {
	Text      string
	Reasoning string
}

// Response is the result of one model call.
type Response struct {
	Text      string
	Reasoning string
	ToolCalls []ToolCall
	Usage     Usage
	Stop      string // stop | tool_calls | length
}

// ModelConfig configures a provider. The zero value reads the provider's
// standard environment variable and uses its default endpoint.
type ModelConfig struct {
	APIKey  string
	BaseURL string
	Headers map[string]string
	Client  *http.Client
	Timeout time.Duration
	// JSONMode selects how structured output reaches the model:
	// "json_schema" (default, native) or "json_object" (schema in the
	// prompt, for providers without native schema support).
	JSONMode string
}

func (c ModelConfig) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: c.timeout()}
}

func (c ModelConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 10 * time.Minute
}

func first(cfg []ModelConfig) ModelConfig {
	if len(cfg) > 0 {
		return cfg[0]
	}
	return ModelConfig{}
}
