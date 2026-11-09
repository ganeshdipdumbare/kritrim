package trace

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// OTLPConfig configures an OTLP/HTTP JSON exporter.
type OTLPConfig struct {
	// Endpoint is the collector base URL, e.g. http://localhost:4318. It
	// defaults to OTEL_EXPORTER_OTLP_TRACES_ENDPOINT or
	// OTEL_EXPORTER_OTLP_ENDPOINT.
	Endpoint string
	Service  string
	Headers  map[string]string
	Client   *http.Client
}

type openSpan struct {
	id    string
	start time.Time
	name  string
	attrs map[string]string
}

type runSpans struct {
	trace string
	root  string
	start time.Time
	spans []map[string]any
	model *openSpan
	tool  *openSpan
}

// OTLP returns hooks that export one trace per run to an OTLP/HTTP collector.
// Export errors are dropped so tracing never breaks a run.
func OTLP(cfg OTLPConfig) kritrim.Hooks {
	if cfg.Endpoint == "" {
		cfg.Endpoint = os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "http://localhost:4318"
	}
	if !strings.Contains(cfg.Endpoint, "/v1/traces") {
		cfg.Endpoint = strings.TrimSuffix(cfg.Endpoint, "/") + "/v1/traces"
	}
	if cfg.Service == "" {
		cfg.Service = "kritrim"
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	e := &otlp{cfg: cfg, runs: map[string]*runSpans{}}
	return kritrim.Hooks{
		BeforeRun: func(ctx context.Context, ev *kritrim.RunEvent) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			id := kritrim.RunIDFrom(ctx)
			if id == "" {
				id = newID()
			}
			e.runs[id] = &runSpans{trace: newID(), root: newSpanID(), start: time.Now()}
			return nil
		},
		AfterRun: func(ctx context.Context, ev *kritrim.RunEvent) {
			e.mu.Lock()
			r := e.runs[kritrim.RunIDFrom(ctx)]
			delete(e.runs, kritrim.RunIDFrom(ctx))
			e.mu.Unlock()
			if r == nil {
				return
			}
			status := map[string]any{"code": 1}
			if ev.Err != nil {
				status = map[string]any{"code": 2, "message": ev.Err.Error()}
			}
			r.spans = append(r.spans, span(r.trace, r.root, "", "run", r.start, time.Now(), 1, nil, status))
			e.send(r.spans)
		},
		BeforeModel: func(ctx context.Context, ev *kritrim.ModelEvent) error {
			e.open(ctx, "model", map[string]string{"messages": itoa(len(ev.Request.Messages))}, true)
			return nil
		},
		AfterModel: func(ctx context.Context, ev *kritrim.ModelEvent) {
			attrs := map[string]string{}
			status := map[string]any{"code": 1}
			if ev.Response != nil {
				attrs["stop"] = ev.Response.Stop
				attrs["tokens.in"] = itoa(ev.Response.Usage.In)
				attrs["tokens.out"] = itoa(ev.Response.Usage.Out)
				attrs["tool_calls"] = itoa(len(ev.Response.ToolCalls))
			}
			e.close(ctx, true, attrs, status)
		},
		BeforeTool: func(ctx context.Context, ev *kritrim.ToolEvent) error {
			e.open(ctx, "tool "+ev.Call.Name, map[string]string{"tool": ev.Call.Name}, false)
			return nil
		},
		AfterTool: func(ctx context.Context, ev *kritrim.ToolEvent) {
			status := map[string]any{"code": 1}
			attrs := map[string]string{"tool": ev.Call.Name}
			if ev.Result != nil && ev.Result.IsError {
				status = map[string]any{"code": 2}
				attrs["error"] = "true"
			}
			e.close(ctx, false, attrs, status)
		},
	}
}

type otlp struct {
	cfg  OTLPConfig
	mu   sync.Mutex
	runs map[string]*runSpans
}

func (e *otlp) open(ctx context.Context, name string, attrs map[string]string, model bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.runs[kritrim.RunIDFrom(ctx)]
	if r == nil {
		return
	}
	s := &openSpan{id: newSpanID(), start: time.Now(), name: name, attrs: attrs}
	if model {
		r.model = s
	} else {
		r.tool = s
	}
}

func (e *otlp) close(ctx context.Context, model bool, attrs map[string]string, status map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.runs[kritrim.RunIDFrom(ctx)]
	if r == nil {
		return
	}
	s := r.tool
	if model {
		s = r.model
	}
	if s == nil {
		return
	}
	if model {
		r.model = nil
	} else {
		r.tool = nil
	}
	r.spans = append(r.spans, span(r.trace, r.root, s.id, s.name, s.start, time.Now(), 1, attrs, status))
}

func (e *otlp) send(spans []map[string]any) {
	body := map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{attr("service.name", e.cfg.Service)}},
		"scopeSpans": []any{map[string]any{
			"scope": map[string]any{"name": "kritrim"},
			"spans": spans,
		}},
	}}}
	b, err := json.Marshal(body)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, e.cfg.Endpoint, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := e.cfg.Client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

func span(trace, parent, id, name string, start, end time.Time, kind int, attrs map[string]string, status map[string]any) map[string]any {
	out := map[string]any{
		"traceId":           trace,
		"spanId":            id,
		"name":              name,
		"kind":              kind,
		"startTimeUnixNano": itoa64(start.UnixNano()),
		"endTimeUnixNano":   itoa64(end.UnixNano()),
		"status":            status,
	}
	if parent != "" {
		out["parentSpanId"] = parent
	}
	if len(attrs) > 0 {
		var list []any
		for k, v := range attrs {
			list = append(list, attr(k, v))
		}
		out["attributes"] = list
	}
	return out
}

func attr(k, v string) map[string]any {
	return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func newSpanID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func itoa(n int) string     { return strconv.FormatInt(int64(n), 10) }
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
