package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// Auth authenticates a request. Use Bearer or AllowAll.
type Auth interface {
	Check(r *http.Request) error
}

// Bearer accepts "Authorization: Bearer <token>". Read the token from the
// environment or a secret store; never hard-code it.
func Bearer(token string) Auth { return bearerAuth(token) }

type bearerAuth string

func (b bearerAuth) Check(r *http.Request) error {
	if r.Header.Get("Authorization") != "Bearer "+string(b) {
		return errors.New("a2a: unauthorized")
	}
	return nil
}

// AllowAll disables authentication. Development only.
func AllowAll() Auth { return allowAll{} }

type allowAll struct{}

func (allowAll) Check(*http.Request) error { return nil }

// TaskStore persists A2A tasks. MemoryTasks is the default; a SQL
// implementation can reuse the same semantics as kritrim sessions.
type TaskStore interface {
	Get(ctx context.Context, id string) (*Task, error)
	Put(ctx context.Context, t *Task) error
}

// MemoryTasks keeps tasks in memory.
func MemoryTasks() TaskStore { return &memTasks{m: map[string]*Task{}} }

type memTasks struct {
	mu sync.RWMutex
	m  map[string]*Task
}

func (s *memTasks) Get(ctx context.Context, id string) (*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.m[id]
	if !ok {
		return nil, nil
	}
	cp := *t
	cp.Artifacts = append([]Artifact(nil), t.Artifacts...)
	cp.History = append([]Message(nil), t.History...)
	return &cp, nil
}

func (s *memTasks) Put(ctx context.Context, t *Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *t
	cp.Artifacts = append([]Artifact(nil), t.Artifacts...)
	cp.History = append([]Message(nil), t.History...)
	s.m[t.ID] = &cp
	return nil
}

// ServerConfig exposes a kritrim agent over A2A.
type ServerConfig struct {
	Card Card
	// Agent builds one agent per A2A context. Reuse a durable session store
	// inside the factory so conversations survive restarts.
	Agent func(contextID string) (*kritrim.Agent, error)
	Tasks TaskStore
	Auth  Auth

	MaxContexts  int
	MaxBodyBytes int64
	MaxDuration  time.Duration
}

// Server is an http.Handler speaking A2A.
type Server struct {
	cfg      ServerConfig
	mu       sync.Mutex
	contexts map[string]*contextEntry
	cancels  map[string]context.CancelFunc
}

type contextEntry struct {
	agent *kritrim.Agent
	used  time.Time
}

// NewServer validates the configuration.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Agent == nil {
		return nil, errors.New("a2a: ServerConfig.Agent is required")
	}
	if cfg.Card.Name == "" {
		return nil, errors.New("a2a: ServerConfig.Card.Name is required")
	}
	if cfg.Auth == nil {
		return nil, errors.New("a2a: ServerConfig.Auth is required (use AllowAll only for development)")
	}
	if cfg.Tasks == nil {
		cfg.Tasks = MemoryTasks()
	}
	if cfg.MaxContexts <= 0 {
		cfg.MaxContexts = 1000
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	if cfg.MaxDuration <= 0 {
		cfg.MaxDuration = 10 * time.Minute
	}
	if cfg.Card.ProtocolVersion == "" {
		cfg.Card.ProtocolVersion = "0.3.0"
	}
	if cfg.Card.PreferredTransport == "" {
		cfg.Card.PreferredTransport = "JSONRPC"
	}
	cfg.Card.Capabilities.Streaming = true
	return &Server{cfg: cfg, contexts: map[string]*contextEntry{}, cancels: map[string]context.CancelFunc{}}, nil
}

// Close closes every cached agent.
func (s *Server) Close() {
	s.mu.Lock()
	agents := make([]*kritrim.Agent, 0, len(s.contexts))
	for _, e := range s.contexts {
		agents = append(agents, e.agent)
	}
	s.contexts = map[string]*contextEntry{}
	s.mu.Unlock()
	for _, a := range agents {
		_ = a.Close()
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == CardPath || r.URL.Path == OldCardPath):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.cfg.Card)
		return
	case r.Method != http.MethodPost:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.cfg.Auth.Check(r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.fail(w, nil, -32700, "parse error")
		return
	}
	switch req.Method {
	case "message/send":
		s.send(w, r, req.ID, req.Params, false)
	case "message/stream":
		s.send(w, r, req.ID, req.Params, true)
	case "tasks/get":
		s.taskGet(w, r, req.ID, req.Params)
	case "tasks/cancel":
		s.taskCancel(w, r, req.ID, req.Params)
	default:
		s.fail(w, req.ID, -32601, "method not found")
	}
}

type sendParams struct {
	Message Message `json:"message"`
}

func (s *Server) send(w http.ResponseWriter, r *http.Request, id json.RawMessage, raw json.RawMessage, stream bool) {
	var p sendParams
	if err := json.Unmarshal(raw, &p); err != nil {
		s.fail(w, id, -32602, "invalid params")
		return
	}
	ctxID := p.Message.ContextID
	if ctxID == "" {
		ctxID = newID()
	}
	taskID := p.Message.TaskID
	if taskID == "" {
		taskID = newID()
	}
	agent, err := s.agent(ctxID)
	if err != nil {
		s.fail(w, id, -32603, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.MaxDuration)
	defer cancel()
	s.mu.Lock()
	s.cancels[taskID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.cancels, taskID)
		s.mu.Unlock()
	}()

	decisions := decisionsFrom(p.Message)
	prompt := p.Message.Text()
	if prompt == "" && len(decisions) == 0 {
		s.fail(w, id, -32602, "message has no text or interrupt responses")
		return
	}

	task, _ := s.cfg.Tasks.Get(ctx, taskID)
	if task == nil {
		task = &Task{ID: taskID, ContextID: ctxID, Status: TaskStatus{State: StateSubmitted, Timestamp: now()}}
	}
	task.History = append(task.History, p.Message)
	task.Status = TaskStatus{State: StateWorking, Timestamp: now()}
	_ = s.cfg.Tasks.Put(ctx, task)

	opts := []kritrim.Option{kritrim.Thread(ctxID)}
	if len(decisions) > 0 {
		opts = append(opts, kritrim.Decisions(decisions))
	}

	if stream {
		s.streamRun(ctx, w, id, agent, prompt, opts, task)
		return
	}
	res, err := agent.Run(ctx, prompt, opts...)
	if err != nil {
		task.Status = TaskStatus{State: StateFailed, Timestamp: now()}
		_ = s.cfg.Tasks.Put(ctx, task)
		s.fail(w, id, -32603, err.Error())
		return
	}
	applyResult(task, res)
	_ = s.cfg.Tasks.Put(context.Background(), task)
	s.result(w, id, task)
}

func (s *Server) streamRun(ctx context.Context, w http.ResponseWriter, id json.RawMessage, agent *kritrim.Agent, prompt string, opts []kritrim.Option, task *Task) {
	fl, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, id, -32603, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	writeEvent := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}
	writeEvent(map[string]any{"kind": "status-update", "taskId": task.ID, "contextId": task.ContextID,
		"status": map[string]any{"state": StateWorking}, "final": false})
	var text strings.Builder
	opts = append(opts, kritrim.OnEvent(func(e kritrim.Event) {
		if e.Type == "text" && e.Text != "" {
			text.WriteString(e.Text)
			writeEvent(map[string]any{"kind": "artifact-update", "taskId": task.ID, "contextId": task.ContextID,
				"artifact": map[string]any{"artifactId": "answer", "name": "answer", "parts": []Part{TextPart(e.Text)}},
				"append":   true})
		}
	}))
	res, err := agent.Run(ctx, prompt, opts...)
	if err != nil {
		task.Status = TaskStatus{State: StateFailed, Timestamp: now()}
		_ = s.cfg.Tasks.Put(context.Background(), task)
		writeEvent(map[string]any{"kind": "status-update", "taskId": task.ID, "contextId": task.ContextID,
			"status": map[string]any{"state": StateFailed, "message": map[string]any{"role": "agent", "parts": []Part{TextPart(err.Error())}}}, "final": true})
		return
	}
	applyResult(task, res)
	_ = s.cfg.Tasks.Put(context.Background(), task)
	final := map[string]any{"kind": "status-update", "taskId": task.ID, "contextId": task.ContextID,
		"status": map[string]any{"state": task.Status.State}, "final": true}
	if task.Status.Message != nil {
		final["status"].(map[string]any)["message"] = task.Status.Message
	}
	writeEvent(final)
}

func applyResult(task *Task, res *kritrim.Result) {
	task.Status = TaskStatus{State: StateCompleted, Timestamp: now()}
	if res.Stop == "interrupt" {
		parts := make([]Part, 0, len(res.Interrupts))
		items := make([]any, 0, len(res.Interrupts))
		for _, in := range res.Interrupts {
			items = append(items, map[string]any{"interruptId": in.ID, "name": in.Name, "reason": in.Args})
		}
		parts = append(parts, DataPart(map[string]any{"interrupts": items}))
		if res.Text != "" {
			parts = append([]Part{TextPart(res.Text)}, parts...)
		}
		task.Status = TaskStatus{State: StateInputRequired, Timestamp: now(),
			Message: &Message{Role: "agent", MessageID: newID(), Parts: parts, TaskID: task.ID, ContextID: task.ContextID}}
		return
	}
	if res.Text != "" {
		task.Artifacts = append(task.Artifacts, Artifact{ArtifactID: "answer", Name: "answer", Parts: []Part{TextPart(res.Text)}})
	}
}

func (s *Server) taskGet(w http.ResponseWriter, r *http.Request, id json.RawMessage, raw json.RawMessage) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" {
		s.fail(w, id, -32602, "id is required")
		return
	}
	t, err := s.cfg.Tasks.Get(r.Context(), p.ID)
	if err != nil || t == nil {
		s.fail(w, id, -32001, "task not found")
		return
	}
	s.result(w, id, t)
}

func (s *Server) taskCancel(w http.ResponseWriter, r *http.Request, id json.RawMessage, raw json.RawMessage) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" {
		s.fail(w, id, -32602, "id is required")
		return
	}
	s.mu.Lock()
	cancel := s.cancels[p.ID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	t, err := s.cfg.Tasks.Get(r.Context(), p.ID)
	if err != nil || t == nil {
		s.fail(w, id, -32001, "task not found")
		return
	}
	t.Status = TaskStatus{State: StateCanceled, Timestamp: now()}
	_ = s.cfg.Tasks.Put(r.Context(), t)
	s.result(w, id, t)
}

func (s *Server) result(w http.ResponseWriter, id json.RawMessage, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": v})
}

func (s *Server) fail(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg}})
}

func (s *Server) agent(ctxID string) (*kritrim.Agent, error) {
	s.mu.Lock()
	if e := s.contexts[ctxID]; e != nil {
		e.used = time.Now()
		s.mu.Unlock()
		return e.agent, nil
	}
	s.mu.Unlock()

	a, err := s.cfg.Agent(ctxID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.contexts[ctxID]; e != nil {
		_ = a.Close() // another request won the race
		e.used = time.Now()
		return e.agent, nil
	}
	if len(s.contexts) >= s.cfg.MaxContexts {
		var oldestID string
		var oldest time.Time
		for id, e := range s.contexts {
			if oldestID == "" || e.used.Before(oldest) {
				oldestID, oldest = id, e.used
			}
		}
		if oldestID != "" {
			_ = s.contexts[oldestID].agent.Close()
			delete(s.contexts, oldestID)
		}
	}
	s.contexts[ctxID] = &contextEntry{agent: a, used: time.Now()}
	return a, nil
}

func decisionsFrom(msg Message) map[string]kritrim.Decision {
	var out map[string]kritrim.Decision
	for _, p := range msg.Parts {
		if p.Kind != "data" {
			continue
		}
		raw, ok := p.Data["interruptResponse"].(map[string]any)
		if !ok {
			continue
		}
		id, _ := raw["interruptId"].(string)
		if id == "" {
			continue
		}
		if out == nil {
			out = map[string]kritrim.Decision{}
		}
		out[id] = decisionFromValue(raw["response"])
	}
	return out
}

func decisionFromValue(v any) kritrim.Decision {
	switch x := v.(type) {
	case bool:
		if x {
			return kritrim.Approve()
		}
		return kritrim.Reject("rejected")
	case string:
		return kritrim.Respond(x)
	case map[string]any:
		if s, ok := x["respond"].(string); ok {
			return kritrim.Respond(s)
		}
		if args, ok := x["args"].(map[string]any); ok {
			return kritrim.Edit(args)
		}
		if approved, ok := x["approved"].(bool); ok {
			if approved {
				return kritrim.Approve()
			}
			reason, _ := x["reason"].(string)
			return kritrim.Reject(reason)
		}
	}
	return kritrim.Reject("unsupported response")
}
