package kritrim

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Cancel stops a run started with the RunID option. It reports whether the
// run was found.
func (a *Agent) Cancel(runID string) bool {
	a.cancelMu.Lock()
	cancel := a.cancels[runID]
	a.cancelMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (a *Agent) registerRun(id string, cancel context.CancelFunc) {
	if id == "" {
		return
	}
	a.cancelMu.Lock()
	if a.cancels == nil {
		a.cancels = map[string]context.CancelFunc{}
	}
	a.cancels[id] = cancel
	a.cancelMu.Unlock()
}

func (a *Agent) unregisterRun(id string) {
	a.cancelMu.Lock()
	delete(a.cancels, id)
	a.cancelMu.Unlock()
}

// Session returns a stored conversation, or nil when it does not exist.
func (a *Agent) Session(ctx context.Context, id string) (*Session, error) {
	return a.session.Load(ctx, id)
}

// Fork copies a session to a new id, including paused state. It fails when
// the target exists or the source does not.
func (a *Agent) Fork(ctx context.Context, from, to string) error {
	if from == "" || to == "" {
		return errors.New("kritrim: Fork needs both ids")
	}
	src, err := a.session.Load(ctx, from)
	if err != nil {
		return err
	}
	if src == nil {
		return fmt.Errorf("kritrim: thread %s does not exist", from)
	}
	cp := copySession(src)
	cp.ID, cp.Version = to, 0
	if err := a.session.Save(ctx, cp); err != nil {
		return err
	}
	return nil
}

// CallTool runs a tool directly, without the model. It records the call in
// the audit log but not in the session history.
func (a *Agent) CallTool(ctx context.Context, name string, args map[string]any, opts ...Option) (string, error) {
	o := resolveOptions(opts)
	a.identity(&o)
	ctx = a.runContext(ctx, o, newID(), newState(nil))
	res, pause := a.executeCall(ctx, o, ToolCall{ID: newID(), Name: name, Args: args}, func(Event) {})
	if pause != nil {
		return "", errors.New("kritrim: tool needs approval; use Run with a thread")
	}
	if res.IsError {
		return res.Content, errors.New(res.Content)
	}
	return res.Content, nil
}

func (a *Agent) idemBegin(key string) (*idemEntry, bool) {
	a.idemMu.Lock()
	defer a.idemMu.Unlock()
	if a.idem == nil {
		a.idem = map[string]*idemEntry{}
	}
	if e := a.idem[key]; e != nil && time.Now().Before(e.expires) {
		return e, false
	}
	e := &idemEntry{done: make(chan struct{}), expires: now().Add(10 * time.Minute)}
	a.idem[key] = e
	return e, true
}

func (a *Agent) idemFinish(key string, e *idemEntry, res *Result, err error) {
	a.idemMu.Lock()
	e.res, e.err = res, err
	e.expires = now().Add(10 * time.Minute)
	close(e.done)
	for k, v := range a.idem {
		if time.Now().After(v.expires) {
			delete(a.idem, k)
		}
	}
	a.idemMu.Unlock()
}
