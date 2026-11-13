package kritrim

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

// ErrOutput means the model did not produce a valid value for Output.
var ErrOutput = errors.New("kritrim: model did not produce a valid structured output")

// Approval is what Config.Approve receives before a risky tool runs.
type Approval struct {
	Tool    string         `json:"tool"`
	CallID  string         `json:"call_id"`
	Args    map[string]any `json:"args"`
	Message string         `json:"message"`
}

// Decision is what Config.Approve returns, or what Decisions supplies when
// resuming a paused thread.
type Decision struct {
	Approve bool           `json:"approve"`
	Args    map[string]any `json:"args,omitempty"`    // replace the call arguments
	Respond string         `json:"respond,omitempty"` // answer without running the tool
	Reason  string         `json:"reason,omitempty"`
	// Pause persists the run and returns interrupts instead of deciding now.
	Pause bool `json:"pause,omitempty"`
}

// Interrupt is one paused approval. Its ID is the tool call ID and is the key
// used by Decisions when resuming.
type Interrupt struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"` // approval
	Tool    string         `json:"tool"`
	CallID  string         `json:"call_id"`
	Args    map[string]any `json:"args,omitempty"`
	Message string         `json:"message,omitempty"`
}

// PausedStep is the half-finished loop step stored with the session.
type PausedStep struct {
	Assistant  Message      `json:"assistant"`
	Calls      []ToolCall   `json:"calls"`
	Results    []ToolResult `json:"results,omitempty"`
	Index      int          `json:"index"`
	Interrupts []Interrupt  `json:"interrupts,omitempty"`
}

// Approve, Reject, Edit, Respond and Pause build decisions. Use the same
// constructors in a Decisions map when resuming.
func Approve() Decision                 { return Decision{Approve: true} }
func Reject(why string) Decision        { return Decision{Reason: why} }
func Edit(args map[string]any) Decision { return Decision{Approve: true, Args: args} }
func Respond(text string) Decision      { return Decision{Respond: text} }
func Pause() Decision                   { return Decision{Pause: true} }

// ---- identity on the tool context ----

type ctxKey int

const (
	keyRun ctxKey = iota
	keyThread
	keyUser
	keyRoles
	keyEnv
	keyState
	keyCall
)

// callIDFrom returns the id of the tool call being run.
func callIDFrom(ctx context.Context) string { s, _ := ctx.Value(keyCall).(string); return s }

// UserFrom returns the run identity a tool was called with.
func UserFrom(ctx context.Context) string { s, _ := ctx.Value(keyUser).(string); return s }

// ThreadFrom returns the conversation id a tool was called with.
func ThreadFrom(ctx context.Context) string { s, _ := ctx.Value(keyThread).(string); return s }

// RunIDFrom returns the id of the current run.
func RunIDFrom(ctx context.Context) string { s, _ := ctx.Value(keyRun).(string); return s }

// RolesFrom returns the roles of the current run.
func RolesFrom(ctx context.Context) []string {
	v, _ := ctx.Value(keyRoles).([]string)
	return v
}

// State is per-session key-value data that is not sent to the model. Tools
// and hooks can read and write it; it is saved with the session.
type State struct {
	mu     sync.Mutex
	values map[string]any
}

func newState(values map[string]any) *State {
	cp := make(map[string]any, len(values))
	for k, v := range values {
		cp[k] = v
	}
	return &State{values: cp}
}

// StateFrom returns the state of the current run.
func StateFrom(ctx context.Context) *State {
	s, _ := ctx.Value(keyState).(*State)
	return s
}

func (s *State) Get(key string) any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

func (s *State) Set(key string, v any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = v
}

func (s *State) Delete(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
}

func (s *State) Snapshot() map[string]any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[string]any, len(s.values))
	for k, v := range s.values {
		cp[k] = v
	}
	return cp
}

// ---- secret redaction ----

type redactor struct {
	mu     sync.RWMutex
	values []string
}

func (r *redactor) add(v string) {
	if len(v) < 4 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, old := range r.values {
		if old == v {
			return
		}
	}
	r.values = append(r.values, v)
	// Longest first, so overlapping secrets redact fully.
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
}

func (r *redactor) redact(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, "[redacted]")
	}
	return s
}

func envOf(ctx context.Context) env {
	if e, ok := ctx.Value(keyEnv).(env); ok && e != nil {
		return e
	}
	return noEnv{}
}

// noEnv makes a built-in tool used outside a run fail clearly instead of
// panicking on a nil interface.
type noEnv struct{}

var errNoEnv = errors.New("kritrim: built-in tool used outside a run")

func (noEnv) HTTP(context.Context, httpRequest) (*httpResponse, error) {
	return nil, errNoEnv
}
func (noEnv) Exec(context.Context, execRequest) (execResult, error) {
	return execResult{}, errNoEnv
}
func (noEnv) ReadFile(context.Context, fsRequest) ([]byte, error) { return nil, errNoEnv }
func (noEnv) WriteFile(context.Context, fsRequest) error          { return errNoEnv }
func (noEnv) Secret(context.Context, string) (string, error)      { return "", errNoEnv }
