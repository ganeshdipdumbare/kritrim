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

// Run runs the agent to completion on a prompt. To resume a paused thread,
// pass an empty prompt, the thread, and Decisions.
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

func (a *Agent) run(ctx context.Context, prompt string, emit func(Event), opts ...Option) (res *Result, err error) {
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

	var sess *Session
	if o.thread != "" {
		sess, err = a.session.Load(ctx, o.thread)
		if err != nil {
			return nil, err
		}
	}
	if sess == nil {
		sess = &Session{ID: o.thread}
	}
	ctx = a.runContext(ctx, o, newID(), newState(sess.Values))

	ev := &RunEvent{Prompt: prompt, Thread: o.thread, User: o.user}
	if herr := a.cfg.Hooks.beforeRun(ctx, ev); herr != nil {
		return nil, herr
	}
	defer func() {
		ev.Result, ev.Err = res, err
		a.cfg.Hooks.afterRun(ctx, ev)
	}()

	paused := sess.Paused != nil || len(sess.Pending) > 0
	if paused {
		if prompt != "" || len(o.images) > 0 {
			return nil, fmt.Errorf("kritrim: thread %s is paused; resume with Decisions and no prompt", o.thread)
		}
		if !allDecided(sess, o.decisions) {
			return a.interruptResult(sess, o), nil
		}
	} else if prompt == "" && len(o.images) == 0 {
		return nil, errors.New("kritrim: nothing to run")
	}

	var msgs []Message
	var ps *PausedStep
	if paused {
		msgs = append([]Message(nil), sess.Messages...)
		ps = sess.Paused
	} else {
		msgs = append(append([]Message(nil), sess.Messages...),
			Message{Role: RoleUser, Content: prompt, Images: o.images})
	}
	a.audit(ctx, o, "run", "", "start")
	res, err = a.loop(ctx, sess, msgs, ps, o, emit)
	return res, err
}

func (a *Agent) runContext(ctx context.Context, o runOptions, runID string, st *State) context.Context {
	ctx = context.WithValue(ctx, keyRun, runID)
	ctx = context.WithValue(ctx, keyThread, o.thread)
	ctx = context.WithValue(ctx, keyUser, o.user)
	ctx = context.WithValue(ctx, keyRoles, o.roles)
	ctx = context.WithValue(ctx, keyEnv, a.env)
	ctx = context.WithValue(ctx, keyState, st)
	return ctx
}

func allDecided(sess *Session, decisions map[string]Decision) bool {
	if len(sess.Pending) == 0 {
		return true
	}
	for _, in := range sess.Pending {
		if _, ok := decisions[in.ID]; !ok {
			return false
		}
	}
	return true
}

func (a *Agent) interruptResult(sess *Session, o runOptions) *Result {
	text := ""
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role == RoleAssistant {
			text = sess.Messages[i].Content
			break
		}
	}
	return &Result{Text: text, Stop: "interrupt", Interrupts: append([]Interrupt(nil), sess.Pending...),
		Messages: append([]Message(nil), sess.Messages...), Thread: o.thread}
}

func (a *Agent) loop(ctx context.Context, sess *Session, msgs []Message, ps *PausedStep, o runOptions, emit func(Event)) (*Result, error) {
	schema, wrapped, err := outputSchema(o.output)
	if err != nil {
		return nil, err
	}
	if o.schema != nil {
		schema, wrapped = o.schema, false
	}

	var usage Usage
	var steps []Step
	var text string
	stop := "stop"

	if ps != nil {
		for i := ps.Index; i < len(ps.Calls); i++ {
			call := ps.Calls[i]
			res, pause := a.executeCall(ctx, o, call, emit)
			if pause != nil {
				return a.pause(ctx, sess, msgs, ps, i, o, emit, usage, steps, text)
			}
			ps.Results = append(ps.Results, res)
			msgs = append(msgs, toolMessage(call, res))
			r := res
			emit(Event{Type: "tool_result", ToolResult: &r, Step: 0})
		}
		steps = append(steps, Step{Text: ps.Assistant.Content, ToolCalls: ps.Calls, ToolResults: ps.Results})
		sess.Pending, sess.Paused = nil, nil
	}

	toolCalls := 0
	for step := 0; ; step++ {
		if step >= a.cfg.Limits.Steps {
			stop = "max_steps"
			break
		}
		emit(Event{Type: "step_start", Step: step})
		req := Request{
			System:      a.cfg.Instructions,
			Tools:       a.cfg.Tools,
			MaxTokens:   a.cfg.maxTokens(),
			Temperature: a.cfg.Temperature,
			ToolChoice:  a.cfg.ToolChoice,
			Schema:      schema,
			SchemaName:  "output",
		}
		req.Messages, err = trimContext(ctx, msgs, a.cfg.Context)
		if err != nil {
			return nil, err
		}
		if herr := a.cfg.Hooks.beforeModel(ctx, &req); herr != nil {
			return nil, herr
		}
		resp, err := a.modelStream(ctx, &req, emit)
		if err != nil {
			a.audit(ctx, o, "model", a.Model(), "error: "+err.Error())
			return nil, err
		}
		a.cfg.Hooks.afterModel(ctx, &req, resp)
		usage = usage.Add(resp.Usage)
		text = resp.Text
		if limit := a.cfg.Limits.Tokens; limit > 0 && usage.Total() > limit {
			return nil, fmt.Errorf("kritrim: token budget %d exceeded (%d used)", limit, usage.Total())
		}
		a.auditModel(ctx, o, usage, resp)

		for i := range resp.ToolCalls {
			if resp.ToolCalls[i].ID == "" {
				resp.ToolCalls[i].ID = "call_" + resp.ToolCalls[i].Name + "_" + newID()[:6]
			}
		}
		assistant := Message{Role: RoleAssistant, Content: resp.Text, ToolCalls: resp.ToolCalls}
		msgs = append(msgs, assistant)
		stepRec := Step{Text: resp.Text, ToolCalls: resp.ToolCalls, Usage: resp.Usage}

		if len(resp.ToolCalls) == 0 {
			steps = append(steps, stepRec)
			emit(Event{Type: "step_end", Step: step, Usage: &resp.Usage})
			break
		}
		emit(Event{Type: "step_end", Step: step, Usage: &resp.Usage})
		for i, call := range resp.ToolCalls {
			toolCalls++
			if toolCalls > a.cfg.Limits.ToolCalls {
				return nil, fmt.Errorf("kritrim: tool call limit %d exceeded", a.cfg.Limits.ToolCalls)
			}
			c := call
			emit(Event{Type: "tool_call", ToolCall: &c, Step: step})
			res, pause := a.executeCall(ctx, o, call, emit)
			if pause != nil {
				ps := &PausedStep{Assistant: assistant, Calls: resp.ToolCalls, Results: stepRec.ToolResults, Index: i}
				return a.pause(ctx, sess, msgs, ps, i, o, emit, usage, steps, text)
			}
			stepRec.ToolResults = append(stepRec.ToolResults, res)
			msgs = append(msgs, toolMessage(call, res))
			r := res
			emit(Event{Type: "tool_result", ToolResult: &r, Step: step})
		}
		steps = append(steps, stepRec)
	}

	if o.output != nil {
		if derr := decodeOutput(o.output, text, wrapped); derr != nil {
			fix := fmt.Sprintf("Your reply was not valid JSON for the required schema (%v). Reply with only the JSON object.", derr)
			req := Request{
				System:      a.cfg.Instructions,
				Messages:    append(append([]Message(nil), msgs...), Message{Role: RoleUser, Content: fix}),
				MaxTokens:   a.cfg.maxTokens(),
				Temperature: a.cfg.Temperature,
				ToolChoice:  "none",
				Schema:      schema,
				SchemaName:  "output",
			}
			resp, rerr := a.modelGenerate(ctx, &req)
			if rerr == nil {
				usage = usage.Add(resp.Usage)
				text = resp.Text
				msgs = append(msgs, Message{Role: RoleAssistant, Content: resp.Text})
			}
			if derr := decodeOutput(o.output, text, wrapped); derr != nil {
				_ = a.saveSession(ctx, sess, msgs, o)
				return nil, fmt.Errorf("%w: %v", ErrOutput, derr)
			}
		}
	}

	if err := a.saveSession(ctx, sess, msgs, o); err != nil {
		return nil, err
	}
	res := &Result{Text: text, Steps: steps, Usage: usage, Stop: stop, Messages: msgs, Thread: o.thread}
	emit(Event{Type: "finish", Usage: &usage})
	a.audit(ctx, o, "run", "", "stop="+stop)
	return res, nil
}

func (a *Agent) pause(ctx context.Context, sess *Session, msgs []Message, ps *PausedStep, index int, o runOptions, emit func(Event), usage Usage, steps []Step, text string) (*Result, error) {
	if o.thread == "" {
		return nil, errors.New("kritrim: a durable pause needs a thread; pass kritrim.Thread(id)")
	}
	ps.Index = index
	ps.Interrupts = a.pendingInterrupts(ps.Calls[index:], o.decisions)
	if len(ps.Interrupts) == 0 {
		// The call that asked to pause is no longer pending (already
		// decided); continue instead of returning an empty interrupt set.
		return nil, errors.New("kritrim: pause requested with no pending approval")
	}
	sess.Pending = ps.Interrupts
	sess.Paused = ps
	sess.Messages = msgs
	sess.Values = StateFrom(ctx).Snapshot()
	if err := a.session.Save(ctx, sess); err != nil {
		return nil, err
	}
	a.audit(ctx, o, "interrupt", "", fmt.Sprintf("pending=%d", len(ps.Interrupts)))
	res := &Result{Text: text, Steps: steps, Usage: usage, Stop: "interrupt",
		Interrupts: append([]Interrupt(nil), ps.Interrupts...), Messages: msgs, Thread: o.thread}
	emit(Event{Type: "finish", Usage: &usage})
	return res, nil
}

func (a *Agent) pendingInterrupts(calls []ToolCall, decisions map[string]Decision) []Interrupt {
	var out []Interrupt
	for i := range calls {
		c := &calls[i]
		if c.ID == "" {
			c.ID = "call_" + c.Name + "_" + newID()[:6]
		}
		if _, ok := decisions[c.ID]; ok {
			continue
		}
		t, ok := a.tool(c.Name)
		if !ok || !t.risky() {
			continue
		}
		out = append(out, Interrupt{ID: c.ID, Name: "approval", Tool: c.Name, CallID: c.ID,
			Args: c.Args, Message: fmt.Sprintf("Approve %s with %s?", c.Name, jsonString(c.Args))})
	}
	return out
}

func (a *Agent) executeCall(ctx context.Context, o runOptions, call ToolCall, emit func(Event)) (ToolResult, *Decision) {
	tool, ok := a.tool(call.Name)
	if !ok {
		return ToolResult{CallID: call.ID, Name: call.Name, Content: "unknown tool " + call.Name, IsError: true}, nil
	}
	var dec *Decision
	if d, ok := o.decisions[call.ID]; ok {
		dec = &d
	}
	if dec == nil && tool.risky() {
		ap := &Approval{Tool: tool.Name, CallID: call.ID, Args: call.Args,
			Message: fmt.Sprintf("Approve %s with %s?", tool.Name, jsonString(call.Args))}
		emit(Event{Type: "approval", Approval: ap})
		if a.cfg.Approve == nil {
			return ToolResult{CallID: call.ID, Name: tool.Name, Content: "rejected: approval required and Config.Approve is nil", IsError: true}, nil
		}
		d, err := a.cfg.Approve(ctx, *ap)
		if err != nil {
			return ToolResult{CallID: call.ID, Name: tool.Name, Content: "approval error: " + err.Error(), IsError: true}, nil
		}
		if d.Pause {
			return ToolResult{}, &d
		}
		dec = &d
	}
	if dec != nil {
		a.audit(ctx, o, "decision", call.Name, fmt.Sprintf("approve=%v reason=%s", dec.Approve, dec.Reason))
		if dec.Respond != "" {
			return ToolResult{CallID: call.ID, Name: tool.Name, Content: a.redactor.redact(dec.Respond)}, nil
		}
		if !dec.Approve && dec.Args == nil {
			why := dec.Reason
			if why == "" {
				why = "rejected"
			}
			return ToolResult{CallID: call.ID, Name: tool.Name, Content: "rejected: " + why, IsError: true}, nil
		}
		if dec.Args != nil {
			call.Args = dec.Args
		}
	}

	te := &ToolEvent{Call: &call, Args: call.Args}
	if a.cfg.Hooks.BeforeTool != nil {
		if err := a.cfg.Hooks.BeforeTool(ctx, te); err != nil {
			return ToolResult{CallID: call.ID, Name: tool.Name, Content: "blocked: " + err.Error(), IsError: true}, nil
		}
		if te.Deny != "" {
			return ToolResult{CallID: call.ID, Name: tool.Name, Content: "blocked: " + te.Deny, IsError: true}, nil
		}
		if te.Args != nil {
			call.Args = te.Args
		}
	}

	tctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	content, err := runTool(tctx, tool, call.Args)
	res := ToolResult{CallID: call.ID, Name: tool.Name, Content: content}
	if err != nil {
		res.Content = "error: " + err.Error()
		res.IsError = true
	}
	res.Content = a.redactor.redact(res.Content)
	a.audit(ctx, o, "tool", tool.Name, "args="+a.redactor.redact(jsonString(call.Args))+" result="+truncate(res.Content, 2000))
	if a.cfg.Hooks.AfterTool != nil {
		a.cfg.Hooks.AfterTool(ctx, &ToolEvent{Call: &call, Result: &res})
	}
	return res, nil
}

func (a *Agent) modelStream(ctx context.Context, req *Request, emit func(Event)) (*Response, error) {
	emitted := false
	for attempt := 0; ; attempt++ {
		resp, err := a.model.Stream(ctx, *req, func(d Delta) {
			emitted = true
			if d.Text != "" {
				emit(Event{Type: "text", Text: d.Text})
			}
			if d.Reasoning != "" {
				emit(Event{Type: "reasoning", Text: d.Reasoning})
			}
		})
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil || emitted || attempt >= a.cfg.Retry.max()-1 || !a.cfg.Retry.retryable(err) {
			return nil, err
		}
		logf(a.cfg, "model retry %d after %v", attempt+1, err)
		a.cfg.Retry.wait(attempt)
	}
}

func (a *Agent) modelGenerate(ctx context.Context, req *Request) (*Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := a.model.Generate(ctx, *req)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil || attempt >= a.cfg.Retry.max()-1 || !a.cfg.Retry.retryable(err) {
			return nil, err
		}
		logf(a.cfg, "model retry %d after %v", attempt+1, err)
		a.cfg.Retry.wait(attempt)
	}
}

func (a *Agent) saveSession(ctx context.Context, sess *Session, msgs []Message, o runOptions) error {
	if o.thread == "" {
		return nil
	}
	sess.Messages = msgs
	sess.Values = StateFrom(ctx).Snapshot()
	return a.session.Save(ctx, sess)
}

func runTool(ctx context.Context, t Tool, args map[string]any) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool panicked: %v", r)
		}
	}()
	return t.run(ctx, args)
}

func toolMessage(call ToolCall, res ToolResult) Message {
	return Message{Role: RoleTool, ToolCallID: call.ID, ToolName: call.Name, Content: res.Content}
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
