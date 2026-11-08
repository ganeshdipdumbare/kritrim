package kritrim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"time"
)

// Run runs the agent to completion on a prompt.
func (a *Agent) Run(ctx context.Context, prompt string, opts ...Option) (*Result, error) {
	return a.run(ctx, prompt, nil, opts...)
}

// Stream runs the agent and yields every event. Breaking out early is safe:
// the run is cancelled and cleaned up.
func (a *Agent) Stream(ctx context.Context, prompt string, opts ...Option) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		ch := make(chan Event, 64)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := a.run(ctx, prompt, func(e Event) {
				select {
				case ch <- e:
				case <-ctx.Done():
				}
			}, opts...)
			done <- err
			close(ch)
		}()
		for e := range ch {
			if !yield(e, nil) {
				cancel()
				for range ch {
				}
				return
			}
		}
		if err := <-done; err != nil {
			yield(Event{Type: "error", Err: err}, err)
		}
	}
}

func (a *Agent) run(ctx context.Context, prompt string, emit func(Event), opts ...Option) (*Result, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	if a.box != nil {
		return a.box.run(ctx, prompt, emit, resolveOptions(opts))
	}
	o := resolveOptions(opts)
	a.identity(&o)
	if o.output != nil && reflect.TypeOf(o.output).Kind() != reflect.Pointer {
		return nil, errors.New("kritrim: Output needs a pointer")
	}
	runID := newID()
	ctx = context.WithValue(ctx, keyRun, runID)
	ctx = context.WithValue(ctx, keyThread, o.thread)
	ctx = context.WithValue(ctx, keyUser, o.user)
	ctx = context.WithValue(ctx, keyRoles, o.roles)
	ctx = context.WithValue(ctx, keyEnv, a.env)

	var sess *Session
	if o.thread != "" {
		var err error
		sess, err = a.session.Load(ctx, o.thread)
		if err != nil {
			return nil, err
		}
	}
	if sess == nil {
		sess = &Session{ID: o.thread}
	}

	msgs := append([]Message(nil), sess.Messages...)
	if prompt != "" || len(o.images) > 0 {
		msgs = append(msgs, Message{Role: RoleUser, Content: prompt, Images: o.images})
	}
	if len(msgs) == 0 {
		return nil, errors.New("kritrim: nothing to run")
	}

	schema, wrapped, err := outputSchema(o.output)
	if err != nil {
		return nil, err
	}
	if o.schema != nil {
		schema, wrapped = o.schema, false
	}
	a.audit(ctx, o, "run", "", "start")

	var usage Usage
	var steps []Step
	var text string
	toolCalls := 0
	stop := "stop"
	tools := a.tools()

	for step := 0; ; step++ {
		if step >= a.cfg.Limits.Steps {
			stop = "max_steps"
			break
		}
		emit(Event{Type: "step_start", Step: step})
		req := Request{
			System:      a.cfg.Instructions,
			Messages:    msgs,
			Tools:       tools,
			MaxTokens:   a.cfg.maxTokens(),
			Temperature: a.cfg.Temperature,
			ToolChoice:  a.cfg.ToolChoice,
			Schema:      schema,
			SchemaName:  "output",
		}
		resp, err := a.model.Stream(ctx, req, func(d Delta) {
			if d.Text != "" {
				emit(Event{Type: "text", Text: d.Text, Step: step})
			}
			if d.Reasoning != "" {
				emit(Event{Type: "reasoning", Text: d.Reasoning, Step: step})
			}
		})
		if err != nil {
			a.audit(ctx, o, "model", a.Model(), "error: "+err.Error())
			return nil, err
		}
		usage = usage.Add(resp.Usage)
		text = resp.Text
		if limit := a.cfg.Limits.Tokens; limit > 0 && usage.Total() > limit {
			return nil, fmt.Errorf("kritrim: token budget %d exceeded (%d used)", limit, usage.Total())
		}
		a.auditModel(ctx, o, usage, resp)

		assistant := Message{Role: RoleAssistant, Content: resp.Text, ToolCalls: resp.ToolCalls}
		msgs = append(msgs, assistant)
		stepRec := Step{Text: resp.Text, ToolCalls: resp.ToolCalls, Usage: resp.Usage}

		if len(resp.ToolCalls) == 0 {
			steps = append(steps, stepRec)
			emit(Event{Type: "step_end", Step: step, Usage: &resp.Usage})
			break
		}

		emit(Event{Type: "step_end", Step: step, Usage: &resp.Usage})
		for _, call := range resp.ToolCalls {
			toolCalls++
			if toolCalls > a.cfg.Limits.ToolCalls {
				return nil, fmt.Errorf("kritrim: tool call limit %d exceeded", a.cfg.Limits.ToolCalls)
			}
			c := call
			emit(Event{Type: "tool_call", ToolCall: &c, Step: step})
			res := a.execute(ctx, o, c, emit)
			stepRec.ToolResults = append(stepRec.ToolResults, res)
			msgs = append(msgs, Message{Role: RoleTool, ToolCallID: c.ID, ToolName: c.Name, Content: res.Content})
			r := res
			emit(Event{Type: "tool_result", ToolResult: &r, Step: step})
		}
		steps = append(steps, stepRec)
	}

	if o.output != nil {
		if err := decodeOutput(o.output, text, wrapped); err != nil {
			// One repair round, with tools off.
			fix := fmt.Sprintf("Your reply was not valid JSON for the required schema (%v). Reply with only the JSON object.", err)
			req := Request{
				System:      a.cfg.Instructions,
				Messages:    append(msgs, Message{Role: RoleUser, Content: fix}),
				MaxTokens:   a.cfg.maxTokens(),
				Temperature: a.cfg.Temperature,
				ToolChoice:  "none",
				Schema:      schema,
				SchemaName:  "output",
			}
			resp, rerr := a.model.Generate(ctx, req)
			if rerr == nil {
				usage = usage.Add(resp.Usage)
				text = resp.Text
				msgs = append(msgs, Message{Role: RoleAssistant, Content: resp.Text})
			}
			if derr := decodeOutput(o.output, text, wrapped); derr != nil {
				a.save(ctx, sess, msgs, o)
				return nil, fmt.Errorf("%w: %v", ErrOutput, derr)
			}
		}
	}

	if o.thread != "" {
		if err := a.save(ctx, sess, msgs, o); err != nil {
			return nil, err
		}
	}
	res := &Result{Text: text, Steps: steps, Usage: usage, Stop: stop, Messages: msgs, Thread: o.thread}
	emit(Event{Type: "finish", Usage: &usage})
	a.audit(ctx, o, "run", "", "stop="+stop)
	return res, nil
}

func (a *Agent) save(ctx context.Context, sess *Session, msgs []Message, o runOptions) error {
	sess.ID = o.thread
	sess.Messages = msgs
	sess.Updated = now()
	return a.session.Save(ctx, sess)
}

func (a *Agent) tools() []Tool { return a.cfg.Tools }

func (a *Agent) execute(ctx context.Context, o runOptions, call ToolCall, emit func(Event)) ToolResult {
	tool, ok := a.tool(call.Name)
	if !ok {
		return ToolResult{CallID: call.ID, Name: call.Name, Content: "unknown tool " + call.Name, IsError: true}
	}
	args := call.Args
	if tool.risky() {
		ap := &Approval{Tool: tool.Name, CallID: call.ID, Args: args,
			Message: fmt.Sprintf("Approve %s with %s?", tool.Name, jsonString(args))}
		emit(Event{Type: "approval", Approval: ap})
		if a.cfg.Approve == nil {
			return ToolResult{CallID: call.ID, Name: call.Name, Content: "rejected: approval required and Config.Approve is nil", IsError: true}
		}
		dec, err := a.cfg.Approve(ctx, *ap)
		if err != nil {
			return ToolResult{CallID: call.ID, Name: call.Name, Content: "approval error: " + err.Error(), IsError: true}
		}
		a.audit(ctx, o, "approval", tool.Name, fmt.Sprintf("approve=%v reason=%s", dec.Approve, dec.Reason))
		if dec.Respond != "" {
			return ToolResult{CallID: call.ID, Name: call.Name, Content: a.redactor.redact(dec.Respond)}
		}
		if !dec.Approve {
			why := dec.Reason
			if why == "" {
				why = "rejected"
			}
			return ToolResult{CallID: call.ID, Name: call.Name, Content: "rejected: " + why, IsError: true}
		}
		if dec.Args != nil {
			args = dec.Args
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	content, err := runTool(ctx, tool, args)
	if err != nil {
		content = "error: " + err.Error()
		return ToolResult{CallID: call.ID, Name: tool.Name, Content: a.redactor.redact(content), IsError: true}
	}
	content = a.redactor.redact(content)
	a.audit(ctx, o, "tool", tool.Name, "args="+a.redactor.redact(jsonString(args))+" result="+truncate(content, 2000))
	return ToolResult{CallID: call.ID, Name: tool.Name, Content: content}
}

func runTool(ctx context.Context, t Tool, args map[string]any) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool panicked: %v", r)
		}
	}()
	return t.run(ctx, args)
}

func (a *Agent) tool(name string) (Tool, bool) {
	for _, t := range a.cfg.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

func (a *Agent) audit(ctx context.Context, o runOptions, kind, name, detail string) {
	if a.cfg.Audit == nil {
		return
	}
	e := Entry{Time: now().UnixMilli(), Kind: kind, Name: name, Detail: a.redactor.redact(detail),
		Run: RunIDFrom(ctx), Thread: o.thread, User: o.user}
	if err := a.cfg.Audit.Write(ctx, e); err != nil {
		logf(a.cfg, "audit: %v", err)
	}
}

func (a *Agent) auditModel(ctx context.Context, o runOptions, total Usage, resp *Response) {
	a.audit(ctx, o, "model", a.Model(), fmt.Sprintf("stop=%s in=%d out=%d total=%d", resp.Stop, resp.Usage.In, resp.Usage.Out, total.Total()))
}

func (c Config) maxTokens() int { return c.MaxOutputTokens }

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func outputSchema(v any) (schema map[string]any, wrapped bool, err error) {
	if v == nil {
		return nil, false, nil
	}
	t := reflect.TypeOf(v)
	if t.Kind() != reflect.Pointer {
		return nil, false, errors.New("kritrim: Output needs a pointer")
	}
	elem := t.Elem()
	s := schemaOf(reflect.New(elem).Interface())
	if s["type"] != "object" {
		return map[string]any{"type": "object", "properties": map[string]any{"value": s},
			"required": []any{"value"}, "additionalProperties": false}, true, nil
	}
	return s, false, nil
}

func decodeOutput(v any, text string, wrapped bool) error {
	s := strings.TrimSpace(text)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		s = strings.TrimPrefix(s, "json")
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	if i := strings.IndexAny(s, "{["); i > 0 {
		s = s[i:]
	}
	s = strings.TrimSpace(s)
	if wrapped {
		var w struct {
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal([]byte(s), &w); err != nil {
			return err
		}
		s = string(w.Value)
	}
	return json.Unmarshal([]byte(s), v)
}
