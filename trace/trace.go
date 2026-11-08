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

// JSON returns hooks that append JSON lines to w.
func JSON(w io.Writer) kritrim.Hooks {
	t := &tracer{enc: json.NewEncoder(w)}
	return kritrim.Hooks{
		BeforeRun: func(ctx context.Context, e *kritrim.RunEvent) error {
			t.write(map[string]any{"event": "run.start", "thread": e.Thread, "user": e.User})
			return nil
		},
		AfterRun: func(ctx context.Context, e *kritrim.RunEvent) {
			out := map[string]any{"event": "run.end", "thread": e.Thread, "user": e.User}
			if e.Result != nil {
				out["stop"] = e.Result.Stop
				out["tokens"] = e.Result.Usage.Total()
			}
			if e.Err != nil {
				out["error"] = e.Err.Error()
			}
			t.write(out)
		},
		BeforeModel: func(ctx context.Context, e *kritrim.ModelEvent) error {
			t.write(map[string]any{"event": "model.start", "system": e.Request.System, "messages": len(e.Request.Messages)})
			return nil
		},
		AfterModel: func(ctx context.Context, e *kritrim.ModelEvent) {
			if e.Response == nil {
				return
			}
			t.write(map[string]any{"event": "model.end", "stop": e.Response.Stop,
				"in": e.Response.Usage.In, "out": e.Response.Usage.Out, "tools": len(e.Response.ToolCalls)})
		},
		BeforeTool: func(ctx context.Context, e *kritrim.ToolEvent) error {
			t.write(map[string]any{"event": "tool.start", "tool": e.Call.Name})
			return nil
		},
		AfterTool: func(ctx context.Context, e *kritrim.ToolEvent) {
			out := map[string]any{"event": "tool.end", "tool": e.Call.Name}
			if e.Result != nil {
				out["error"] = e.Result.IsError
			}
			t.write(out)
		},
	}
}

type tracer struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (t *tracer) write(v map[string]any) {
	v["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.enc.Encode(v)
}
