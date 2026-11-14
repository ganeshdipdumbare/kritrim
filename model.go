package kritrim

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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
	// Cache asks providers that support it to cache the system prompt and
	// tools (Anthropic cache_control).
	Cache bool
	// Reasoning asks the model to think before answering. Providers that
	// do not support it ignore the field.
	Reasoning *Reasoning
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
	// ReasoningSig is the provider's signature over Reasoning, replayed
	// with the thinking block on the next call.
	ReasoningSig string
	ToolCalls    []ToolCall
	Usage        Usage
	Stop         string // stop | tool_calls | length
}

// ModelConfig configures a provider. The zero value reads the provider's
// standard environment variable and uses its default endpoint.
type ModelConfig struct {
	APIKey  string
	BaseURL string
	Headers map[string]string
	Client  *http.Client
	Timeout time.Duration
	// Region selects the AWS region for Bedrock.
	Region string
	// AWS credentials for Bedrock; empty falls back to the standard
	// environment variables.
	AccessKey    string
	SecretKey    string
	SessionToken string
	// JSONMode selects how structured output reaches the model:
	// "json_schema" (default, native) or "json_object" (schema in the
	// prompt, for providers without native schema support).
	JSONMode string
	// Guardrail applies a Bedrock guardrail to every call (Converse
	// guardrailConfig). Other providers refuse it at kritrim.New; use
	// guard.Bedrock to check any model's traffic with ApplyGuardrail.
	Guardrail *Guardrail
}

// Guardrail names a provider guardrail. When it intervenes the reply is the
// guardrail's canned message and Result.Stop is StopGuardrail.
type Guardrail struct {
	ID      string // guardrail identifier or ARN
	Version string // a version number or "DRAFT"
	Trace   bool
}

// invalidModel is a model whose configuration was refused. kritrim.New
// reports the error; a direct call returns it.
type invalidModel struct{ err error }

func (m *invalidModel) Generate(context.Context, Request) (*Response, error) { return nil, m.err }
func (m *invalidModel) Stream(context.Context, Request, func(Delta)) (*Response, error) {
	return nil, m.err
}

// noGuardrail refuses ModelConfig.Guardrail on a provider without one, so a
// guardrail is never silently ignored.
func noGuardrail(provider string, c ModelConfig, m Model) Model {
	if c.Guardrail != nil {
		return &invalidModel{fmt.Errorf("kritrim: %s has no provider guardrail; ModelConfig.Guardrail is Bedrock only (use guard.Bedrock for other models)", provider)}
	}
	return m
}

// checkModels reports the first refused model configuration.
func checkModels(models ...Model) error {
	for _, m := range models {
		if im, ok := m.(*invalidModel); ok {
			return im.err
		}
	}
	return nil
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

// StatusError is a non-2xx provider response. Retry uses Code to decide
// whether another attempt may succeed.
type StatusError struct {
	Provider string
	Code     int
	Status   string
	Body     string
}

// Error reports the provider, status and body.
func (e *StatusError) Error() string {
	return "kritrim: " + e.Provider + ": " + e.Status + ": " + strings.TrimSpace(e.Body)
}
