// Package trigger runs an agent from a schedule or an HTTP webhook.
package trigger

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// Schedule is one recurring run.
type Schedule struct {
	Every  time.Duration
	Thread string
	Prompt string
	User   string
	Roles  []string
}

// Scheduler runs schedules against one agent. A schedule skips a tick when
// its previous run is still going.
type Scheduler struct {
	agent     *kritrim.Agent
	schedules []Schedule
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	mu        sync.Mutex
	running   []bool
	log       func(msg string)
}

// NewScheduler validates the schedules.
func NewScheduler(agent *kritrim.Agent, schedules ...Schedule) (*Scheduler, error) {
	if agent == nil {
		return nil, errors.New("trigger: agent is required")
	}
	for i, s := range schedules {
		if s.Every <= 0 {
			return nil, fmt.Errorf("trigger: schedule %d needs Every > 0", i)
		}
	}
	return &Scheduler{agent: agent, schedules: schedules, running: make([]bool, len(schedules))}, nil
}

// SetLogger receives run errors.
func (s *Scheduler) SetLogger(fn func(msg string)) { s.log = fn }

// Start begins the schedules and returns immediately.
func (s *Scheduler) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	for i, sc := range s.schedules {
		s.wg.Add(1)
		go s.loop(ctx, i, sc)
	}
}

func (s *Scheduler) loop(ctx context.Context, i int, sc Schedule) {
	defer s.wg.Done()
	t := time.NewTicker(sc.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !s.begin(i) {
				continue
			}
			_, err := s.agent.Run(ctx, sc.Prompt, kritrim.Thread(sc.Thread),
				kritrim.User(sc.User), kritrim.Roles(sc.Roles...))
			s.end(i)
			if err != nil && ctx.Err() == nil && s.log != nil {
				s.log("schedule " + sc.Thread + ": " + err.Error())
			}
		}
	}
}

func (s *Scheduler) begin(i int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[i] {
		return false
	}
	s.running[i] = true
	return true
}

func (s *Scheduler) end(i int) {
	s.mu.Lock()
	s.running[i] = false
	s.mu.Unlock()
}

// Close stops the schedules and waits for running ticks.
func (s *Scheduler) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

// WebhookConfig exposes an agent at one HTTP path.
type WebhookConfig struct {
	Agent *kritrim.Agent
	// Secret is the HMAC key, literal or "secret://NAME". Required.
	Secret string
	// Thread maps a request body to a thread id. Default: "webhook".
	Thread func(body []byte) string
	// Prompt maps a request body to a prompt. Default: the body as text, or
	// its "prompt"/"message" field.
	Prompt func(body []byte) string
	// MaxBody defaults to 1 MiB.
	MaxBody int64
}

// Webhook returns an http.Handler that verifies X-Kritrim-Signature and runs
// the agent. Idempotency-Key deduplicates retries in memory.
func Webhook(cfg WebhookConfig) (http.Handler, error) {
	if cfg.Agent == nil {
		return nil, errors.New("trigger: webhook needs an agent")
	}
	secret := cfg.Secret
	if strings.HasPrefix(secret, "secret://") {
		secret = os.Getenv(strings.TrimPrefix(secret, "secret://"))
	}
	if secret == "" {
		return nil, errors.New("trigger: webhook needs a Secret")
	}
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = 1 << 20
	}
	if cfg.Thread == nil {
		cfg.Thread = func([]byte) string { return "webhook" }
	}
	if cfg.Prompt == nil {
		cfg.Prompt = defaultPrompt
	}
	h := &webhook{cfg: cfg, secret: []byte(secret), seen: map[string]string{}}
	return h, nil
}

type webhook struct {
	cfg    WebhookConfig
	secret []byte
	mu     sync.Mutex
	seen   map[string]string
}

func (h *webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBody)
	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if !h.valid(r.Header.Get("X-Kritrim-Signature"), body) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" {
		h.mu.Lock()
		if prev, ok := h.seen[key]; ok {
			h.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"text": prev, "duplicate": true})
			return
		}
		h.mu.Unlock()
	}
	res, err := h.cfg.Agent.Run(r.Context(), h.cfg.Prompt(body), kritrim.Thread(h.cfg.Thread(body)))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if key != "" {
		h.mu.Lock()
		if len(h.seen) > 10000 { // shortcut: fixed cap, use a TTL store for long-lived servers
			h.seen = map[string]string{}
		}
		h.seen[key] = res.Text
		h.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": res.Text, "thread": res.Thread, "stop": res.Stop})
}

func (h *webhook) valid(sig string, body []byte) bool {
	mac := hmac.New(sha256.New, h.secret)
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(want))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func defaultPrompt(body []byte) string {
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		for _, k := range []string{"prompt", "message", "text"} {
			if s, ok := m[k].(string); ok {
				return s
			}
		}
	}
	return string(body)
}
