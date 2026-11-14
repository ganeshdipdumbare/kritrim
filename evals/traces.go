package evals

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// traceLine is one line written by trace.JSON.
type traceLine struct {
	Event    string         `json:"event"`
	Time     time.Time      `json:"time"`
	Run      string         `json:"run"`
	Thread   string         `json:"thread"`
	Prompt   string         `json:"prompt"`
	Text     string         `json:"text"`
	Stop     string         `json:"stop"`
	Tokens   int            `json:"tokens"`
	Error    any            `json:"error"`
	In       int            `json:"in"`
	Out      int            `json:"out"`
	Tool     string         `json:"tool"`
	Call     string         `json:"call"`
	Args     map[string]any `json:"args"`
	Result   string         `json:"result"`
	Messages int            `json:"messages"`
}

// FromTrace rebuilds one Result per run from the JSON lines trace.JSON
// writes, so recorded production traffic can be scored with
// Experiment.Score. Tool trajectories, stops, token counts and detectors
// work on any trace; evaluators that read the answer or tool arguments
// need a trace written with trace.Content(). Runs appear in the order they
// started; a run with no run.end line is reported with an error.
func FromTrace(r io.Reader) ([]Result, error) {
	type building struct {
		res   *Result
		start time.Time
		ended bool
	}
	runs := map[string]*building{}
	var order []string
	get := func(id string) *building {
		b, ok := runs[id]
		if !ok {
			b = &building{res: &Result{Case: Case{Name: id}, Run: &kritrim.Result{RunID: id, Stop: kritrim.StopDone}}}
			runs[id] = b
			order = append(order, id)
		}
		return b
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var l traceLine
		if err := json.Unmarshal(line, &l); err != nil {
			return nil, fmt.Errorf("evals: trace line %d: %w", n, err)
		}
		b := get(l.Run)
		run := b.res.Run
		switch l.Event {
		case "run.start":
			b.start = l.Time
			run.Thread = l.Thread
			if l.Thread != "" {
				b.res.Case.Name = l.Thread + "/" + l.Run
			}
			b.res.Case.Input = l.Prompt
		case "model.end":
			run.Steps = append(run.Steps, kritrim.Step{Text: l.Text, Usage: kritrim.Usage{In: l.In, Out: l.Out}})
			run.Usage = run.Usage.Add(kritrim.Usage{In: l.In, Out: l.Out})
			run.Text = l.Text
		case "tool.end":
			if len(run.Steps) == 0 {
				run.Steps = append(run.Steps, kritrim.Step{})
			}
			st := &run.Steps[len(run.Steps)-1]
			st.ToolCalls = append(st.ToolCalls, kritrim.ToolCall{ID: l.Call, Name: l.Tool, Args: l.Args})
			isErr, _ := l.Error.(bool)
			st.ToolResults = append(st.ToolResults, kritrim.ToolResult{CallID: l.Call, Name: l.Tool, Content: l.Result, IsError: isErr})
		case "run.end":
			b.ended = true
			if l.Stop != "" {
				run.Stop = l.Stop
			}
			if l.Text != "" {
				run.Text = l.Text
			}
			if msg, ok := l.Error.(string); ok && msg != "" {
				b.res.Err = msg
			}
			if !b.start.IsZero() && !l.Time.IsZero() {
				b.res.Duration = l.Time.Sub(b.start)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(order))
	for _, id := range order {
		b := runs[id]
		r := *b.res
		if !b.ended && r.Err == "" {
			r.Err = "trace has no run.end for this run"
		}
		r.Output = r.Run.Text
		r.Tokens = r.Run.Usage.Total()
		if r.Err != "" {
			r.Run = nil
		}
		out = append(out, r)
	}
	return out, nil
}
