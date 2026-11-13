package kritrim

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"strings"
	"time"
)

// Hooks are optional callbacks around the loop. They run after the built-in
// security checks, so a hook can observe or shape a call but cannot grant a
// capability that policy denied.
type Hooks struct {
	BeforeRun   func(ctx context.Context, e *RunEvent) error
	AfterRun    func(ctx context.Context, e *RunEvent)
	BeforeModel func(ctx context.Context, e *ModelEvent) error
	AfterModel  func(ctx context.Context, e *ModelEvent)
	BeforeTool  func(ctx context.Context, e *ToolEvent) error
	AfterTool   func(ctx context.Context, e *ToolEvent)
}

// RunEvent describes one run.
type RunEvent struct {
	Prompt string
	Thread string
	User   string
	Result *Result
	Err    error
}

// ModelEvent describes one model call. Mutations to Request or Response are
// used by the loop.
type ModelEvent struct {
	Request  *Request
	Response *Response
}

// ToolEvent describes one tool call. BeforeTool may set Deny to skip it.
type ToolEvent struct {
	Call   *ToolCall
	Args   map[string]any
	Result *ToolResult
	Deny   string
}

func (h Hooks) beforeRun(ctx context.Context, e *RunEvent) error {
	if h.BeforeRun != nil {
		return h.BeforeRun(ctx, e)
	}
	return nil
}

func (h Hooks) afterRun(ctx context.Context, e *RunEvent) {
	if h.AfterRun != nil {
		h.AfterRun(ctx, e)
	}
}

func (h Hooks) beforeModel(ctx context.Context, req *Request) error {
	if h.BeforeModel != nil {
		return h.BeforeModel(ctx, &ModelEvent{Request: req})
	}
	return nil
}

func (h Hooks) afterModel(ctx context.Context, req *Request, resp *Response) {
	if h.AfterModel != nil {
		h.AfterModel(ctx, &ModelEvent{Request: req, Response: resp})
	}
}

// Retry configures model-call retries. The zero value means three attempts
// with exponential backoff on rate limits, 5xx and network errors.
type Retry struct {
	Max      int
	Base     time.Duration
	MaxDelay time.Duration
	RetryOn  func(error) bool
}

func (r *Retry) max() int {
	if r == nil || r.Max <= 0 {
		return 3
	}
	return r.Max
}

func (r *Retry) base() time.Duration {
	if r == nil || r.Base <= 0 {
		return 250 * time.Millisecond
	}
	return r.Base
}

func (r *Retry) maxDelay() time.Duration {
	if r == nil || r.MaxDelay <= 0 {
		return 5 * time.Second
	}
	return r.MaxDelay
}

func (r *Retry) retryable(err error) bool {
	if err == nil {
		return false
	}
	if r != nil && r.RetryOn != nil {
		return r.RetryOn(err)
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code == 408 || se.Code == 409 || se.Code == 429 || se.Code >= 500
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	// Providers that do not return StatusError still fail with recognizable
	// text; keep the fallback narrow.
	s := strings.ToLower(err.Error())
	for _, part := range []string{"rate limit", "overloaded", "timeout", "temporarily unavailable"} {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

func (r *Retry) wait(attempt int) {
	d := r.base() << attempt
	if max := r.maxDelay(); d > max {
		d = max
	}
	// Full jitter spreads retries from concurrent runs.
	d = time.Duration(rand.Int64N(int64(d) + 1))
	time.Sleep(d)
}

// ContextConfig keeps the prompt within a token budget. It is off by
// default. Summarize, when set, turns dropped messages into replacements.
type ContextConfig struct {
	MaxTokens int
	KeepLast  int
	Summarize func(ctx context.Context, dropped []Message) ([]Message, error)
}

// estimateTokens is a coarse four-characters-per-token estimate.
func estimateTokens(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)/4 + 8
		for _, c := range m.ToolCalls {
			n += len(c.Name)/4 + len(jsonString(c.Args))/4
		}
	}
	return n
}

// trimContext drops old messages while keeping tool pairs valid.
func trimContext(ctx context.Context, msgs []Message, cfg *ContextConfig) ([]Message, error) {
	if cfg == nil || cfg.MaxTokens <= 0 || estimateTokens(msgs) <= cfg.MaxTokens {
		return msgs, nil
	}
	keep := cfg.KeepLast
	if keep <= 0 {
		keep = 20
	}
	start := len(msgs) - keep
	if start < 0 {
		start = 0
	}
	for start < len(msgs) && msgs[start].Role == RoleTool {
		start++
	}
	for start < len(msgs) && msgs[start].Role == RoleAssistant {
		start++
	}
	if start <= 0 {
		return msgs, nil
	}
	kept := append([]Message(nil), msgs[start:]...)
	if cfg.Summarize != nil {
		dropped := append([]Message(nil), msgs[:start]...)
		repl, err := cfg.Summarize(ctx, dropped)
		if err != nil {
			return nil, err
		}
		kept = append(append([]Message(nil), repl...), kept...)
	}
	return kept, nil
}

// Chain combines hooks into one value for Config.Hooks, e.g.
//
//	Hooks: kritrim.Chain(guard.PII("email"), memory.Inject(store, 5), trace.OTel(cfg))
//
// Every hook runs in the order given, for Before and After alike, and each
// sees the changes made by the ones before it. A Before hook that returns
// an error (or a BeforeTool hook that sets Deny) stops the chain. List
// guards that redact or deny first, so observers see the guarded values.
func Chain(hooks ...Hooks) Hooks {
	var out Hooks
	for _, h := range hooks {
		if h.BeforeRun != nil {
			out.BeforeRun = chainBefore(out.BeforeRun, h.BeforeRun)
		}
		if h.AfterRun != nil {
			out.AfterRun = chainAfter(out.AfterRun, h.AfterRun)
		}
		if h.BeforeModel != nil {
			out.BeforeModel = chainBefore(out.BeforeModel, h.BeforeModel)
		}
		if h.AfterModel != nil {
			out.AfterModel = chainAfter(out.AfterModel, h.AfterModel)
		}
		if h.BeforeTool != nil {
			out.BeforeTool = chainBeforeTool(out.BeforeTool, h.BeforeTool)
		}
		if h.AfterTool != nil {
			out.AfterTool = chainAfter(out.AfterTool, h.AfterTool)
		}
	}
	return out
}

func chainBefore[E any](prev, next func(context.Context, E) error) func(context.Context, E) error {
	if prev == nil {
		return next
	}
	return func(ctx context.Context, e E) error {
		if err := prev(ctx, e); err != nil {
			return err
		}
		return next(ctx, e)
	}
}

func chainBeforeTool(prev, next func(context.Context, *ToolEvent) error) func(context.Context, *ToolEvent) error {
	if prev == nil {
		return next
	}
	return func(ctx context.Context, e *ToolEvent) error {
		if err := prev(ctx, e); err != nil || e.Deny != "" {
			return err
		}
		return next(ctx, e)
	}
}

func chainAfter[E any](prev, next func(context.Context, E)) func(context.Context, E) {
	if prev == nil {
		return next
	}
	return func(ctx context.Context, e E) {
		prev(ctx, e)
		next(ctx, e)
	}
}
