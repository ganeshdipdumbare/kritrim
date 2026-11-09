package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// OTel returns hooks that export traces, metrics and logs over OTLP/HTTP
// JSON. OTLP exports traces only.
func OTel(cfg OTLPConfig) kritrim.Hooks {
	base := otelBase(cfg)
	traceCfg := cfg
	traceCfg.Endpoint = base + "/v1/traces"
	th := OTLP(traceCfg)
	if cfg.Service == "" {
		cfg.Service = "kritrim"
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	o := &otel{cfg: cfg, metrics: base + "/v1/metrics",
		logs: base + "/v1/logs", runs: map[string]*otelRun{}}
	return kritrim.Hooks{
		BeforeRun: func(ctx context.Context, e *kritrim.RunEvent) error {
			if th.BeforeRun != nil {
				_ = th.BeforeRun(ctx, e)
			}
			id := kritrim.RunIDFrom(ctx)
			o.mu.Lock()
			o.runs[id] = &otelRun{start: time.Now(), toolStart: map[string]time.Time{}}
			o.mu.Unlock()
			o.log(ctx, 9, "run.start", map[string]string{"thread": e.Thread, "user": e.User})
			return nil
		},
		AfterRun: func(ctx context.Context, e *kritrim.RunEvent) {
			if th.AfterRun != nil {
				th.AfterRun(ctx, e)
			}
			id := kritrim.RunIDFrom(ctx)
			o.mu.Lock()
			r := o.runs[id]
			delete(o.runs, id)
			o.mu.Unlock()
			if r == nil {
				return
			}
			stop := ""
			if e.Result != nil {
				stop = e.Result.Stop
				o.point(r, metricPoint{name: "kritrim.runs", unit: "1", value: 1, integer: true,
					attrs: map[string]string{"stop": stop}})
			}
			sev := 9
			if e.Err != nil {
				sev = 17
			}
			r.logs = append(r.logs, logRecord{at: time.Now(), severity: sev, text: "run.end",
				attrs: map[string]string{"stop": stop, "thread": e.Thread}})
			o.flush(r)
		},
		BeforeModel: func(ctx context.Context, e *kritrim.ModelEvent) error {
			if th.BeforeModel != nil {
				_ = th.BeforeModel(ctx, e)
			}
			o.mu.Lock()
			if r := o.runs[kritrim.RunIDFrom(ctx)]; r != nil {
				r.modelStart = time.Now()
			}
			o.mu.Unlock()
			return nil
		},
		AfterModel: func(ctx context.Context, e *kritrim.ModelEvent) {
			if th.AfterModel != nil {
				th.AfterModel(ctx, e)
			}
			id := kritrim.RunIDFrom(ctx)
			o.mu.Lock()
			r := o.runs[id]
			if r == nil {
				o.mu.Unlock()
				return
			}
			dur := time.Since(r.modelStart).Milliseconds()
			model := e.Request.Model
			if e.Response != nil {
				r.points = append(r.points, metricPoint{name: "kritrim.model.calls", unit: "1", value: 1, integer: true,
					attrs: map[string]string{"model": model, "stop": e.Response.Stop}})
				r.points = append(r.points,
					metricPoint{name: "kritrim.model.tokens", unit: "1", value: float64(e.Response.Usage.In), integer: true,
						attrs: map[string]string{"model": model, "kind": "input"}},
					metricPoint{name: "kritrim.model.tokens", unit: "1", value: float64(e.Response.Usage.Out), integer: true,
						attrs: map[string]string{"model": model, "kind": "output"}})
				r.logs = append(r.logs, logRecord{at: time.Now(), severity: 9, text: "model",
					attrs: map[string]string{"model": model, "stop": e.Response.Stop}})
			}
			r.points = append(r.points, metricPoint{name: "kritrim.model.duration_ms", unit: "ms", value: float64(dur)})
			o.mu.Unlock()
		},
		BeforeTool: func(ctx context.Context, e *kritrim.ToolEvent) error {
			if th.BeforeTool != nil {
				_ = th.BeforeTool(ctx, e)
			}
			o.mu.Lock()
			if r := o.runs[kritrim.RunIDFrom(ctx)]; r != nil && e.Call != nil {
				r.toolStart[e.Call.ID] = time.Now()
			}
			o.mu.Unlock()
			return nil
		},
		AfterTool: func(ctx context.Context, e *kritrim.ToolEvent) {
			if th.AfterTool != nil {
				th.AfterTool(ctx, e)
			}
			id := kritrim.RunIDFrom(ctx)
			o.mu.Lock()
			r := o.runs[id]
			if r == nil || e.Call == nil {
				o.mu.Unlock()
				return
			}
			dur := time.Since(r.toolStart[e.Call.ID]).Milliseconds()
			delete(r.toolStart, e.Call.ID)
			errAttr := "false"
			if e.Result != nil && e.Result.IsError {
				errAttr = "true"
			}
			r.points = append(r.points,
				metricPoint{name: "kritrim.tool.calls", unit: "1", value: 1, integer: true,
					attrs: map[string]string{"tool": e.Call.Name, "error": errAttr}},
				metricPoint{name: "kritrim.tool.duration_ms", unit: "ms", value: float64(dur)})
			sev := 9
			if errAttr == "true" {
				sev = 17
			}
			r.logs = append(r.logs, logRecord{at: time.Now(), severity: sev, text: "tool",
				attrs: map[string]string{"tool": e.Call.Name, "error": errAttr}})
			o.mu.Unlock()
		},
	}
}

func otelBase(cfg OTLPConfig) string {
	base := cfg.Endpoint
	if base == "" {
		base = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if base == "" {
		base = "http://localhost:4318"
	}
	for _, suffix := range []string{"/v1/traces", "/v1/metrics", "/v1/logs"} {
		base = strings.TrimSuffix(strings.TrimSuffix(base, "/"), suffix)
	}
	return base
}

type metricPoint struct {
	name    string
	unit    string
	value   float64
	integer bool
	attrs   map[string]string
}

type logRecord struct {
	at       time.Time
	severity int
	text     string
	attrs    map[string]string
}

type otelRun struct {
	start      time.Time
	modelStart time.Time
	toolStart  map[string]time.Time
	points     []metricPoint
	logs       []logRecord
}

type otel struct {
	cfg     OTLPConfig
	metrics string
	logs    string
	mu      sync.Mutex
	runs    map[string]*otelRun
}

func (o *otel) point(r *otelRun, p metricPoint) {
	o.mu.Lock()
	r.points = append(r.points, p)
	o.mu.Unlock()
}

func (o *otel) log(ctx context.Context, sev int, text string, attrs map[string]string) {
	o.mu.Lock()
	if r := o.runs[kritrim.RunIDFrom(ctx)]; r != nil {
		r.logs = append(r.logs, logRecord{at: time.Now(), severity: sev, text: text, attrs: attrs})
	}
	o.mu.Unlock()
}

func (o *otel) flush(r *otelRun) {
	o.mu.Lock()
	points := append([]metricPoint(nil), r.points...)
	logs := append([]logRecord(nil), r.logs...)
	o.mu.Unlock()
	if len(points) > 0 {
		o.send(o.metrics, o.metricsBody(points, r.start))
	}
	if len(logs) > 0 {
		o.send(o.logs, o.logsBody(logs))
	}
}

func (o *otel) metricsBody(points []metricPoint, start time.Time) map[string]any {
	byName := map[string][]metricPoint{}
	var order []string
	for _, p := range points {
		if _, ok := byName[p.name]; !ok {
			order = append(order, p.name)
		}
		byName[p.name] = append(byName[p.name], p)
	}
	var metrics []any
	for _, name := range order {
		ps := byName[name]
		var dps []any
		integer := ps[0].integer
		for _, p := range ps {
			dp := map[string]any{"timeUnixNano": strconv.FormatInt(time.Now().UnixNano(), 10)}
			if p.attrs != nil {
				var attrs []any
				for k, v := range p.attrs {
					attrs = append(attrs, attr(k, v))
				}
				dp["attributes"] = attrs
			}
			if integer {
				dp["asInt"] = strconv.FormatInt(int64(p.value), 10)
				dp["startTimeUnixNano"] = strconv.FormatInt(start.UnixNano(), 10)
			} else {
				dp["asDouble"] = p.value
			}
			dps = append(dps, dp)
		}
		m := map[string]any{"name": name, "unit": ps[0].unit}
		if integer {
			m["sum"] = map[string]any{"aggregationTemporality": 2, "isMonotonic": true, "dataPoints": dps}
		} else {
			m["gauge"] = map[string]any{"dataPoints": dps}
		}
		metrics = append(metrics, m)
	}
	return map[string]any{"resourceMetrics": []any{map[string]any{
		"resource":     map[string]any{"attributes": []any{attr("service.name", o.cfg.Service)}},
		"scopeMetrics": []any{map[string]any{"scope": map[string]any{"name": "kritrim"}, "metrics": metrics}},
	}}}
}

func (o *otel) logsBody(logs []logRecord) map[string]any {
	var records []any
	for _, l := range logs {
		sevText := "INFO"
		if l.severity >= 17 {
			sevText = "ERROR"
		}
		rec := map[string]any{
			"timeUnixNano":   strconv.FormatInt(l.at.UnixNano(), 10),
			"severityNumber": l.severity,
			"severityText":   sevText,
			"body":           map[string]any{"stringValue": l.text},
		}
		if len(l.attrs) > 0 {
			var attrs []any
			for k, v := range l.attrs {
				attrs = append(attrs, attr(k, v))
			}
			rec["attributes"] = attrs
		}
		records = append(records, rec)
	}
	return map[string]any{"resourceLogs": []any{map[string]any{
		"resource":  map[string]any{"attributes": []any{attr("service.name", o.cfg.Service)}},
		"scopeLogs": []any{map[string]any{"scope": map[string]any{"name": "kritrim"}, "logRecords": records}},
	}}}
}

func (o *otel) send(url string, body map[string]any) {
	b, err := json.Marshal(body)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range o.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := o.cfg.Client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}
