package a2a

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
// environment or a secret store; never hard-code it. NewServer refuses an
// empty token, and an empty token never authenticates.
func Bearer(token string) Auth { return bearerAuth(token) }

type bearerAuth string

func (b bearerAuth) Check(r *http.Request) error {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	want := sha256.Sum256([]byte(b))
	have := sha256.Sum256([]byte(got))
	if b == "" || !ok || subtle.ConstantTimeCompare(want[:], have[:]) != 1 {
		return errors.New("a2a: unauthorized")
	}
	return nil
}

// Principal is implemented by an Auth that can tell callers apart. When the
// server's Auth implements it, every task, context and push config belongs
// to the caller that created it, and other callers get "task not found".
type Principal interface {
	Principal(r *http.Request) string
}

// Tokens accepts several bearer tokens, each naming a principal (tenant,
// service or user). Tasks and contexts are isolated per principal.
//
//	Auth: a2a.Tokens(map[string]string{os.Getenv("TOKEN_A"): "billing", os.Getenv("TOKEN_B"): "support"})
func Tokens(tokens map[string]string) Auth {
	t := tokenAuth{}
	for tok, who := range tokens {
		t = append(t, tokenEntry{sum: sha256.Sum256([]byte(tok)), who: who, empty: strings.TrimSpace(tok) == "" || who == ""})
	}
	return t
}

type tokenEntry struct {
	sum   [32]byte
	who   string
	empty bool
}

type tokenAuth []tokenEntry

func (t tokenAuth) match(r *http.Request) (string, bool) {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || got == "" {
		return "", false
	}
	have := sha256.Sum256([]byte(got))
	who, found := "", false
	for _, e := range t { // no early exit: constant time in the token count
		if subtle.ConstantTimeCompare(e.sum[:], have[:]) == 1 && !e.empty {
			who, found = e.who, true
		}
	}
	return who, found
}

func (t tokenAuth) Check(r *http.Request) error {
	if _, ok := t.match(r); !ok {
		return errors.New("a2a: unauthorized")
	}
	return nil
}

func (t tokenAuth) Principal(r *http.Request) string { who, _ := t.match(r); return who }

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
	// MaxRunsPerPrincipal caps how many runs one principal has in flight at
	// once. Default 8. Each run is an LLM session that costs money.
	MaxRunsPerPrincipal int

	// Push stores task webhooks. Default MemoryPush.
	Push PushStore
	// PushAllowPrivate permits push URLs on private addresses. Tests and
	// internal deployments only; it is an SSRF surface.
	PushAllowPrivate bool
	PushClient       *http.Client

	// MaxFileBytes caps the total size of the file parts in one message,
	// inline or fetched. Default 1 MB; inline bytes are also bounded by
	// MaxBodyBytes.
	MaxFileBytes int
	// FileHosts lists the hosts a file part's URI may be fetched from,
	// e.g. "files.example.com" or "*.example.com". Empty means URIs are
	// refused. Exact entries may be private; wildcards must be public.
	FileHosts  []string
	FileClient *http.Client
}

// Server is an http.Handler speaking A2A.
type Server struct {
	cfg        ServerConfig
	mu         sync.Mutex
	contexts   map[string]*contextEntry
	cancels    map[string]context.CancelFunc
	ctxBusy    map[string]bool // principal+context with a run in flight
	inflight   map[string]int  // principal -> runs in flight
	taskMu     sync.Mutex      // serializes task status read-modify-write
	pushClient *http.Client
	fileClient *http.Client
}

type contextEntry struct {
	agent *kritrim.Agent
	used  time.Time
	who   string
	busy  int // runs in flight; a busy entry is never evicted
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
	if b, ok := cfg.Auth.(bearerAuth); ok && strings.TrimSpace(string(b)) == "" {
		return nil, errors.New("a2a: Bearer token is empty")
	}
	if t, ok := cfg.Auth.(tokenAuth); ok {
		if len(t) == 0 {
			return nil, errors.New("a2a: Tokens needs at least one token")
		}
		for _, e := range t {
			if e.empty {
				return nil, errors.New("a2a: Tokens has an empty token or principal")
			}
		}
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
	if cfg.MaxRunsPerPrincipal <= 0 {
		cfg.MaxRunsPerPrincipal = 8
	}
	if cfg.Card.ProtocolVersion == "" {
		cfg.Card.ProtocolVersion = "0.3.0"
	}
	if cfg.Card.PreferredTransport == "" {
		cfg.Card.PreferredTransport = "JSONRPC"
	}
	if cfg.Push == nil {
		cfg.Push = MemoryPush()
	}
	if cfg.PushClient == nil {
		cfg.PushClient = &http.Client{Timeout: 5 * time.Second}
	}
	pushClient, err := pushHTTPClient(cfg.PushClient, cfg.PushAllowPrivate)
	if err != nil {
		return nil, err
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = 1 << 20
	}
	if cfg.FileClient == nil {
		cfg.FileClient = &http.Client{Timeout: 30 * time.Second}
	}
	fileClient, err := fileHTTPClient(cfg.FileClient, cfg.FileHosts)
	if err != nil {
		return nil, err
	}
	cfg.Card.Capabilities.Streaming = true
	cfg.Card.Capabilities.PushNotifications = true
	return &Server{cfg: cfg, contexts: map[string]*contextEntry{}, cancels: map[string]context.CancelFunc{},
		ctxBusy: map[string]bool{}, inflight: map[string]int{},
		pushClient: pushClient, fileClient: fileClient}, nil
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

// ServeHTTP serves the agent card and the JSON-RPC methods.
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
	case "tasks/pushNotificationConfig/set":
		s.pushSet(w, r, req.ID, req.Params)
	case "tasks/pushNotificationConfig/get":
		s.pushGet(w, r, req.ID, req.Params)
	case "tasks/pushNotificationConfig/list":
		s.pushList(w, r, req.ID, req.Params)
	case "tasks/pushNotificationConfig/delete":
		s.pushDelete(w, r, req.ID, req.Params)
	default:
		s.fail(w, req.ID, -32601, "method not found")
	}
}

type sendParams struct {
	Message       Message `json:"message"`
	Configuration *struct {
		PushNotificationConfig *PushConfig `json:"pushNotificationConfig,omitempty"`
	} `json:"configuration,omitempty"`
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
	who := s.principal(r)
	// Fail closed: a store error is not a reason to skip the owner check.
	existing, err := s.cfg.Tasks.Get(r.Context(), taskID)
	if err != nil {
		s.internal(w, id, "task lookup", err)
		return
	}
	if existing != nil && existing.Owner != who {
		s.fail(w, id, -32001, "task not found")
		return
	}
	if p.Configuration != nil && p.Configuration.PushNotificationConfig != nil {
		if err := s.validatePush(r.Context(), *p.Configuration.PushNotificationConfig); err != nil {
			s.fail(w, id, -32602, err.Error())
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.MaxDuration)
	defer cancel()

	// Validate the message before allocating anything: a refused request
	// must not create an agent or touch the context cache.
	decisions := decisionsFrom(p.Message)
	files, images, err := s.inputFiles(ctx, p.Message)
	if err != nil {
		s.fail(w, id, -32602, err.Error())
		return
	}
	prompt := p.Message.Text() + files
	if prompt == "" && len(images) == 0 && len(decisions) == 0 {
		s.fail(w, id, -32602, "message has no text, files or interrupt responses")
		return
	}

	// One run per task, and one per context, at a time. A second run on
	// either would overwrite the first's cancel function or interleave its
	// history on one thread. Each principal also has a cap on runs in flight,
	// since the task and context IDs are chosen by the client.
	ctxKey := who + "\x00" + ctxID
	s.mu.Lock()
	switch {
	case s.cancels[taskID] != nil:
		s.mu.Unlock()
		s.fail(w, id, -32000, "task already has an active run")
		return
	case s.ctxBusy[ctxKey]:
		s.mu.Unlock()
		s.fail(w, id, -32000, "context already has an active run")
		return
	case s.inflight[who] >= s.cfg.MaxRunsPerPrincipal:
		s.mu.Unlock()
		s.fail(w, id, -32000, "too many concurrent runs for this principal")
		return
	}
	s.cancels[taskID] = cancel
	s.ctxBusy[ctxKey] = true
	s.inflight[who]++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.cancels, taskID)
		delete(s.ctxBusy, ctxKey)
		s.inflight[who]--
		if s.inflight[who] == 0 {
			delete(s.inflight, who)
		}
		s.mu.Unlock()
	}()

	agent, release, err := s.agent(who, ctxID)
	if err != nil {
		s.internal(w, id, "agent", err)
		return
	}
	defer release()

	// Ownership is checked again on the record this request changes. The
	// lookup above can be stale, and a failed lookup must never look like a
	// new task that replaces someone else's.
	task, err := s.cfg.Tasks.Get(ctx, taskID)
	if err != nil {
		s.internal(w, id, "task lookup", err)
		return
	}
	if task != nil && task.Owner != who {
		s.fail(w, id, -32001, "task not found")
		return
	}
	if task == nil {
		task = &Task{ID: taskID, ContextID: ctxID, Owner: who, Status: TaskStatus{State: StateSubmitted, Timestamp: now()}}
	}
	if p.Configuration != nil && p.Configuration.PushNotificationConfig != nil {
		_ = s.cfg.Push.Set(ctx, taskID, *p.Configuration.PushNotificationConfig)
	}
	task.History = append(task.History, p.Message)
	if n := len(task.History); n > maxTaskHistory {
		task.History = append([]Message(nil), task.History[n-maxTaskHistory:]...)
	}
	task.Status = TaskStatus{State: StateWorking, Timestamp: now()}
	// A new message starts fresh, so this is a plain Put, not save: a
	// canceled state from an earlier run must not carry over.
	_ = s.cfg.Tasks.Put(ctx, task)
	s.notify(ctx, task)

	opts := []kritrim.Option{kritrim.Thread(threadID(who, ctxID))}
	if len(decisions) > 0 {
		opts = append(opts, kritrim.Decisions(decisions))
	}
	if len(images) > 0 {
		opts = append(opts, kritrim.Images(images...))
	}

	if stream {
		s.streamRun(ctx, w, id, agent, prompt, opts, task)
		return
	}
	res, err := agent.Run(ctx, prompt, opts...)
	if err != nil {
		task.Status = TaskStatus{State: StateFailed, Timestamp: now()}
		s.save(context.Background(), task)
		s.internal(w, id, "run "+taskID, err)
		return
	}
	applyResult(task, res)
	s.save(context.Background(), task)
	s.notify(context.Background(), task)
	s.result(w, id, task)
}

// internal logs the detail and tells the client only that something failed.
// Errors from the agent factory and the store can name hosts, paths and
// connection strings, so they stay on the server.
func (s *Server) internal(w http.ResponseWriter, id json.RawMessage, what string, err error) {
	log.Printf("a2a: %s: %v", what, err)
	s.fail(w, id, -32603, "internal error")
}

// save stores the task, keeping a cancellation that tasks/cancel recorded
// while the run was going. The run must not overwrite it with "completed".
// The read and write happen under the task lock, so a cancel cannot land
// between them.
func (s *Server) save(ctx context.Context, task *Task) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if cur, err := s.cfg.Tasks.Get(ctx, task.ID); err == nil && cur != nil && cur.Status.State == StateCanceled {
		task.Status = cur.Status
	}
	_ = s.cfg.Tasks.Put(ctx, task)
}

// Limits on what one task keeps. The client chooses how many messages it
// sends, so the server bounds what it stores.
const (
	maxTaskHistory   = 200
	maxTaskArtifacts = 50
)

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
		if e.Type == kritrim.EventText && e.Text != "" {
			text.WriteString(e.Text)
			writeEvent(map[string]any{"kind": "artifact-update", "taskId": task.ID, "contextId": task.ContextID,
				"artifact": map[string]any{"artifactId": "answer", "name": "answer", "parts": []Part{TextPart(e.Text)}},
				"append":   true})
		}
	}))
	res, err := agent.Run(ctx, prompt, opts...)
	if err != nil {
		task.Status = TaskStatus{State: StateFailed, Timestamp: now()}
		s.save(context.Background(), task)
		log.Printf("a2a: run %s: %v", task.ID, err)
		writeEvent(map[string]any{"kind": "status-update", "taskId": task.ID, "contextId": task.ContextID,
			"status": map[string]any{"state": StateFailed, "message": map[string]any{"role": "agent", "parts": []Part{TextPart("agent run failed")}}}, "final": true})
		return
	}
	applyResult(task, res)
	s.save(context.Background(), task)
	s.notify(context.Background(), task)
	final := map[string]any{"kind": "status-update", "taskId": task.ID, "contextId": task.ContextID,
		"status": map[string]any{"state": task.Status.State}, "final": true}
	if task.Status.Message != nil {
		final["status"].(map[string]any)["message"] = task.Status.Message
	}
	writeEvent(final)
}

func applyResult(task *Task, res *kritrim.Result) {
	task.Status = TaskStatus{State: StateCompleted, Timestamp: now()}
	if res.Stop == kritrim.StopInterrupt {
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
		if n := len(task.Artifacts); n > maxTaskArtifacts {
			task.Artifacts = append([]Artifact(nil), task.Artifacts[n-maxTaskArtifacts:]...)
		}
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
	t := s.ownedTask(r, p.ID)
	if t == nil {
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
	t := s.ownedTask(r, p.ID)
	if t == nil {
		s.fail(w, id, -32001, "task not found")
		return
	}
	s.mu.Lock()
	cancel := s.cancels[p.ID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Read and write under the task lock, on a fresh copy: a stale copy
	// would write back old history and artifacts over the run's changes.
	s.taskMu.Lock()
	cur := s.ownedTask(r, p.ID)
	if cur == nil {
		s.taskMu.Unlock()
		s.fail(w, id, -32001, "task not found")
		return
	}
	cur.Status = TaskStatus{State: StateCanceled, Timestamp: now()}
	_ = s.cfg.Tasks.Put(r.Context(), cur)
	s.taskMu.Unlock()
	s.notify(r.Context(), cur)
	s.result(w, id, cur)
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

// principal is the caller's identity, or "" when Auth cannot tell callers
// apart.
func (s *Server) principal(r *http.Request) string {
	if p, ok := s.cfg.Auth.(Principal); ok {
		return p.Principal(r)
	}
	return ""
}

// ownedTask returns the task if it exists and belongs to the caller.
func (s *Server) ownedTask(r *http.Request, taskID string) *Task {
	t, err := s.cfg.Tasks.Get(r.Context(), taskID)
	if err != nil || t == nil || t.Owner != s.principal(r) {
		return nil
	}
	return t
}

// threadID keeps each principal's conversations apart in the session store.
func threadID(who, ctxID string) string {
	if who == "" {
		return ctxID
	}
	return who + ":" + ctxID
}

// agent returns the cached agent for this principal's context. The agent
// stays busy until release is called, so no other request can evict or
// close it mid-run.
func (s *Server) agent(who, ctxID string) (*kritrim.Agent, func(), error) {
	key := who + "\x00" + ctxID
	s.mu.Lock()
	if e := s.contexts[key]; e != nil {
		e.used = time.Now()
		e.busy++
		s.mu.Unlock()
		return e.agent, s.releaser(e), nil
	}
	s.mu.Unlock()

	a, err := s.cfg.Agent(ctxID)
	if err != nil {
		return nil, nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.contexts[key]; e != nil {
		_ = a.Close() // another request won the race
		e.used = time.Now()
		e.busy++
		return e.agent, s.releaser(e), nil
	}
	if len(s.contexts) >= s.cfg.MaxContexts {
		// At the cap, a principal may only evict its own idle contexts.
		// Evicting another principal's context would close their agent.
		var oldestID string
		var oldest time.Time
		for id, e := range s.contexts {
			if e.who == who && e.busy == 0 && (oldestID == "" || e.used.Before(oldest)) {
				oldestID, oldest = id, e.used
			}
		}
		if oldestID == "" {
			_ = a.Close()
			return nil, nil, errors.New("too many active contexts")
		}
		_ = s.contexts[oldestID].agent.Close()
		delete(s.contexts, oldestID)
	}
	e := &contextEntry{agent: a, used: time.Now(), who: who, busy: 1}
	s.contexts[key] = e
	return a, s.releaser(e), nil
}

// releaser returns the func that marks a context idle again.
func (s *Server) releaser(e *contextEntry) func() {
	return func() {
		s.mu.Lock()
		e.busy--
		s.mu.Unlock()
	}
}

func decisionsFrom(msg Message) map[string]kritrim.Decision {
	var out map[string]kritrim.Decision
	for _, p := range msg.Parts {
		if p.Kind != KindData {
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
