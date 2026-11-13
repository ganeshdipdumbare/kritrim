package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim/internal/netguard"
)

// PushConfig is a webhook the server calls when a task changes. It comes
// from the remote client, so header values are sent as given: "secret://"
// references are never resolved against the server's environment.
type PushConfig struct {
	URL     string            `json:"url"`
	Token   string            `json:"token,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// PushStore keeps one push config per task.
type PushStore interface {
	Set(ctx context.Context, taskID string, cfg PushConfig) error
	Get(ctx context.Context, taskID string) (*PushConfig, error)
	Delete(ctx context.Context, taskID string) error
	List(ctx context.Context, taskID string) ([]PushConfig, error)
}

// MemoryPush keeps push configs in memory.
func MemoryPush() PushStore { return &memPush{m: map[string]PushConfig{}} }

type memPush struct {
	mu sync.RWMutex
	m  map[string]PushConfig
}

func (s *memPush) Set(ctx context.Context, taskID string, cfg PushConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[taskID] = cfg
	return nil
}

func (s *memPush) Get(ctx context.Context, taskID string) (*PushConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.m[taskID]
	if !ok {
		return nil, nil
	}
	return &c, nil
}

func (s *memPush) Delete(ctx context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, taskID)
	return nil
}

func (s *memPush) List(ctx context.Context, taskID string) ([]PushConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c, ok := s.m[taskID]; ok {
		return []PushConfig{c}, nil
	}
	return nil, nil
}

// notify sends the task to the configured webhook. Errors are dropped: a
// push failure must not fail the RPC that produced the task.
func (s *Server) notify(ctx context.Context, task *Task) {
	if s.cfg.Push == nil || task == nil {
		return
	}
	cfg, err := s.cfg.Push.Get(ctx, task.ID)
	if err != nil || cfg == nil {
		return
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return
	}
	if !s.cfg.PushAllowPrivate {
		if err := netguard.CheckHost(ctx, u.Hostname()); err != nil {
			return
		}
	}
	body, err := json.Marshal(task)
	if err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		req.Header.Set("X-A2A-Notification-Token", cfg.Token)
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.pushClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

// pushHTTPClient never follows redirects (a redirect is an SSRF hop) and,
// unless private targets are allowed, re-checks the connected IP at dial
// time so DNS rebinding cannot reach an internal address.
func pushHTTPClient(base *http.Client, allowPrivate bool) (*http.Client, error) {
	c := *base
	if !allowPrivate {
		guarded, ok := netguard.Client(base, nil)
		if !ok {
			return nil, errors.New("a2a: PushClient must use an *http.Transport so push connections can be checked (or set PushAllowPrivate)")
		}
		c = *guarded
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c, nil
}

// validatePush is the one check every push-config write goes through.
func (s *Server) validatePush(ctx context.Context, cfg PushConfig) error {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("pushNotificationConfig.url must be an http(s) URL")
	}
	if !s.cfg.PushAllowPrivate && netguard.CheckHost(ctx, u.Hostname()) != nil {
		return errors.New("pushNotificationConfig.url must be a public host")
	}
	return nil
}

func (s *Server) pushSet(w http.ResponseWriter, r *http.Request, id json.RawMessage, raw json.RawMessage) {
	var p struct {
		TaskID string     `json:"taskId"`
		Config PushConfig `json:"pushNotificationConfig"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.TaskID == "" || p.Config.URL == "" {
		s.fail(w, id, -32602, "taskId and pushNotificationConfig.url are required")
		return
	}
	if s.ownedTask(r, p.TaskID) == nil {
		s.fail(w, id, -32001, "task not found")
		return
	}
	if err := s.validatePush(r.Context(), p.Config); err != nil {
		s.fail(w, id, -32602, err.Error())
		return
	}
	if err := s.cfg.Push.Set(r.Context(), p.TaskID, p.Config); err != nil {
		s.fail(w, id, -32603, err.Error())
		return
	}
	s.result(w, id, map[string]any{})
}

func (s *Server) pushGet(w http.ResponseWriter, r *http.Request, id json.RawMessage, raw json.RawMessage) {
	var p struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.TaskID == "" {
		s.fail(w, id, -32602, "taskId is required")
		return
	}
	if s.ownedTask(r, p.TaskID) == nil {
		s.fail(w, id, -32001, "task not found")
		return
	}
	cfg, err := s.cfg.Push.Get(r.Context(), p.TaskID)
	if err != nil {
		s.fail(w, id, -32603, err.Error())
		return
	}
	if cfg == nil {
		s.fail(w, id, -32001, "push config not found")
		return
	}
	s.result(w, id, cfg)
}

func (s *Server) pushList(w http.ResponseWriter, r *http.Request, id json.RawMessage, raw json.RawMessage) {
	var p struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.TaskID == "" {
		s.fail(w, id, -32602, "taskId is required")
		return
	}
	if s.ownedTask(r, p.TaskID) == nil {
		s.fail(w, id, -32001, "task not found")
		return
	}
	list, err := s.cfg.Push.List(r.Context(), p.TaskID)
	if err != nil {
		s.fail(w, id, -32603, err.Error())
		return
	}
	if list == nil {
		list = []PushConfig{}
	}
	s.result(w, id, map[string]any{"configs": list})
}

func (s *Server) pushDelete(w http.ResponseWriter, r *http.Request, id json.RawMessage, raw json.RawMessage) {
	var p struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.TaskID == "" {
		s.fail(w, id, -32602, "taskId is required")
		return
	}
	if s.ownedTask(r, p.TaskID) == nil {
		s.fail(w, id, -32001, "task not found")
		return
	}
	if err := s.cfg.Push.Delete(r.Context(), p.TaskID); err != nil {
		s.fail(w, id, -32603, err.Error())
		return
	}
	s.result(w, id, map[string]any{})
}

// SetPush registers a webhook for a task.
func (c *Client) SetPush(ctx context.Context, taskID string, cfg PushConfig) error {
	var out map[string]any
	return c.call(ctx, "tasks/pushNotificationConfig/set",
		map[string]any{"taskId": taskID, "pushNotificationConfig": cfg}, &out)
}

// GetPush returns a task's webhook.
func (c *Client) GetPush(ctx context.Context, taskID string) (*PushConfig, error) {
	var out PushConfig
	if err := c.call(ctx, "tasks/pushNotificationConfig/get", map[string]any{"taskId": taskID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeletePush removes a task's webhook.
func (c *Client) DeletePush(ctx context.Context, taskID string) error {
	var out map[string]any
	return c.call(ctx, "tasks/pushNotificationConfig/delete", map[string]any{"taskId": taskID}, &out)
}
