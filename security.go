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

// Decision is what Config.Approve returns.
type Decision struct {
	Approve bool           `json:"approve"`
	Args    map[string]any `json:"args,omitempty"`    // replace the call arguments
	Respond string         `json:"respond,omitempty"` // answer without running the tool
	Reason  string         `json:"reason,omitempty"`
}

// Approve and Reject build decisions.
func Approve() Decision          { return Decision{Approve: true} }
func Reject(why string) Decision { return Decision{Reason: why} }

// ---- identity on the tool context ----

type ctxKey int

const (
	keyRun ctxKey = iota
	keyThread
	keyUser
	keyRoles
	keyEnv
)

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
