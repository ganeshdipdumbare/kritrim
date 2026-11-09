package kritrim

import "time"

// Role is the author of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn in a conversation.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
	Images     []Image    `json:"images,omitempty"`
}

// Image is an image attached to a user message.
type Image struct {
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
}

// ToolCall is one tool invocation requested by the model.
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// ToolResult is the outcome of one tool call, sent back to the model.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

// Usage is token accounting for one model call.
type Usage struct {
	In  int `json:"in"`
	Out int `json:"out"`
}

// Add returns u + o.
func (u Usage) Add(o Usage) Usage { return Usage{In: u.In + o.In, Out: u.Out + o.Out} }

// Total is the tokens counted against a budget.
func (u Usage) Total() int { return u.In + u.Out }

// Step is one model call and the tool calls it produced.
type Step struct {
	Text        string       `json:"text,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
	Usage       Usage        `json:"usage"`
}

// Event is emitted while a run streams. Type names the kind:
// text, reasoning, tool_call, tool_result, approval, step_start, step_end,
// finish, error.
type Event struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	Reasoning  string      `json:"reasoning,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	Approval   *Approval   `json:"approval,omitempty"`
	Step       int         `json:"step,omitempty"`
	Usage      *Usage      `json:"usage,omitempty"`
	Err        error       `json:"-"`
}

// Result is the outcome of a run.
type Result struct {
	Text       string      `json:"text"`
	Steps      []Step      `json:"steps"`
	Usage      Usage       `json:"usage"`
	Stop       string      `json:"stop"` // stop | max_steps | interrupt | stopped
	Interrupts []Interrupt `json:"interrupts,omitempty"`
	Paused     *PausedStep `json:"paused,omitempty"`
	Messages   []Message   `json:"messages"`
	Thread     string      `json:"thread,omitempty"`
	RunID      string      `json:"run_id,omitempty"`
}

// ToolCalls returns every tool call made during the run.
func (r *Result) ToolCalls() []ToolCall {
	var out []ToolCall
	for _, s := range r.Steps {
		out = append(out, s.ToolCalls...)
	}
	return out
}

// Clock is the time source; tests replace it.
var now = time.Now
