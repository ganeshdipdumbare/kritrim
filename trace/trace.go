// Package trace writes one JSON line per lifecycle event. It is a dependency
// free observer; wire the same lines into your log or metrics pipeline, or
// use the hooks directly with an OpenTelemetry adapter.
package trace

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// Option configures JSON.
type Option func(*options)

type options struct{ content bool }

// Content also writes what was said: the prompt, the system prompt, each
// reply, tool arguments and tool results (truncated to 4 KB). It makes a
// trace evaluable offline (kritrim-evals traces), and it makes the trace
// as sensitive as the conversation itself.
func Content() Option { return func(o *options) { o.content = true } }

// JSON returns hooks that append JSON lines to w. Every line carries the
// event, the time and the run id; without Content no conversation text is
// written.
func JSON(w io.Writer, opts ...Option) kritrim.Hooks {
	var o options
	for _, fn := range opts {
		fn(&o)
	}
	t := &tracer{enc: json.NewEncoder(w)}
	return kritrim.Hooks{
		BeforeRun: func(ctx context.Context, e *kritrim.RunEvent) error {
			out := map[string]any{"event": "run.start", "thread": e.Thread, "user": e.User}
			if o.content {
				out["prompt"] = e.Prompt
			}
			t.write(ctx, out)
			return nil
		},
		AfterRun: func(ctx context.Context, e *kritrim.RunEvent) {
			out := map[string]any{"event": "run.end", "thread": e.Thread, "user": e.User}
			if e.Result != nil {
				out["stop"] = e.Result.Stop
				out["tokens"] = e.Result.Usage.Total()
				if o.content {
					out["text"] = e.Result.Text
				}
			}
			if e.Err != nil {
				out["error"] = e.Err.Error()
			}
			t.write(ctx, out)
		},
		BeforeModel: func(ctx context.Context, e *kritrim.ModelEvent) error {
			out := map[string]any{"event": "model.start", "messages": len(e.Request.Messages)}
			if o.content {
				out["system"] = e.Request.System
			}
			t.write(ctx, out)
			return nil
		},
		AfterModel: func(ctx context.Context, e *kritrim.ModelEvent) {
			if e.Response == nil {
				return
			}
			out := map[string]any{"event": "model.end", "stop": e.Response.Stop,
				"in": e.Response.Usage.In, "out": e.Response.Usage.Out, "tools": len(e.Response.ToolCalls)}
			if o.content {
				out["text"] = e.Response.Text
			}
			t.write(ctx, out)
		},
		BeforeTool: func(ctx context.Context, e *kritrim.ToolEvent) error {
			out := map[string]any{"event": "tool.start", "tool": e.Call.Name, "call": e.Call.ID}
			if o.content {
				out["args"] = e.Args
			}
			t.write(ctx, out)
			return nil
		},
		AfterTool: func(ctx context.Context, e *kritrim.ToolEvent) {
			out := map[string]any{"event": "tool.end", "tool": e.Call.Name, "call": e.Call.ID}
			if e.Result != nil {
				out["error"] = e.Result.IsError
				if o.content {
					out["args"] = e.Call.Args
					out["result"] = truncate(e.Result.Content, 4096)
				}
			}
			t.write(ctx, out)
		},
	}
}

type tracer struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (t *tracer) write(ctx context.Context, v map[string]any) {
	v["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	if id := kritrim.RunIDFrom(ctx); id != "" {
		v["run"] = id
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.enc.Encode(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
