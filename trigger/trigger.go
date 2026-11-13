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
	"io"
	"net/http"
	"os"
	"strconv"
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
	// Tolerance is how far X-Kritrim-Timestamp may be from now. Default 5m.
	Tolerance time.Duration
	// DedupeTTL is how long a delivery is remembered. Default 24h; it is
	// never shorter than twice Tolerance.
	DedupeTTL time.Duration
	// MaxEntries bounds the dedupe memory. Default 100000. When it is full
	// of unexpired deliveries the webhook answers 503 with Retry-After.
	MaxEntries int
	// Logger receives run errors; callers only see "run failed".
	Logger func(msg string)
}

// Header names used by Webhook and Sign.
const (
	SignatureHeader   = "X-Kritrim-Signature"
	TimestampHeader   = "X-Kritrim-Timestamp"
	IdempotencyHeader = "Idempotency-Key"
)

// Sign returns the X-Kritrim-Signature value for a delivery. The MAC covers
// the timestamp, the idempotency key and the body, so none can be changed
// or replayed outside the tolerance window.
//
//	ts := time.Now()
//	req.Header.Set(trigger.TimestampHeader, strconv.FormatInt(ts.Unix(), 10))
//	req.Header.Set(trigger.IdempotencyHeader, key)
//	req.Header.Set(trigger.SignatureHeader, trigger.Sign(secret, ts, key, body))
func Sign(secret string, ts time.Time, key string, body []byte) string {
	return sign([]byte(secret), strconv.FormatInt(ts.Unix(), 10), key, body)
}

// sign MACs a versioned, length-prefixed encoding of the fields, so no two
// (timestamp, key, body) triples share a MAC input.
func sign(secret []byte, ts, key string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "kritrim-webhook-v1\n%d:%s\n%d:%s\n%d:", len(ts), ts, len(key), key, len(body))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Webhook returns an http.Handler that verifies a timestamped HMAC signature
// and runs the agent once per delivery. A delivery is identified by its
// Idempotency-Key, or by its signature when there is none; duplicates,
// including concurrent ones, get the first result.
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
	if cfg.Tolerance <= 0 {
		cfg.Tolerance = 5 * time.Minute
	}
	if cfg.DedupeTTL <= 0 {
		cfg.DedupeTTL = 24 * time.Hour
	}
	if cfg.DedupeTTL < 2*cfg.Tolerance {
		cfg.DedupeTTL = 2 * cfg.Tolerance
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 100_000
	}
	if cfg.Thread == nil {
		cfg.Thread = func([]byte) string { return "webhook" }
	}
	if cfg.Prompt == nil {
		cfg.Prompt = defaultPrompt
	}
	h := &webhook{cfg: cfg, secret: []byte(secret), seen: map[string]*delivery{}}
	return h, nil
}

type webhook struct {
	cfg    WebhookConfig
	secret []byte
	mu     sync.Mutex
	seen   map[string]*delivery
}

type delivery struct {
	done    chan struct{}
	print   string // fingerprint of the signed request
	out     map[string]any
	failed  bool
	expires time.Time
}

// errBusy means the dedupe memory is full of deliveries that are still
// running or still replayable; the sender should retry later.
var errBusy = errors.New("trigger: webhook busy")

func (h *webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	ts := r.Header.Get(TimestampHeader)
	key := r.Header.Get(IdempotencyHeader)
	sig := r.Header.Get(SignatureHeader)
	if !h.fresh(ts) || !hmac.Equal([]byte(sig), []byte(sign(h.secret, ts, key, body))) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	if !json.Valid(body) {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	// Separate namespaces so a key can never collide with a signature.
	id := "sig:" + sig
	if key != "" {
		id = "key:" + key
	}
	sum := sha256.Sum256(body)
	d, leader, err := h.begin(id, hex.EncodeToString(sum[:]))
	switch {
	case errors.Is(err, errBusy):
		w.Header().Set("Retry-After", "5")
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	case err != nil:
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if !leader {
		select {
		case <-d.done:
		case <-r.Context().Done():
			return
		}
		if d.failed {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "run failed"})
			return
		}
		out := map[string]any{"duplicate": true}
		for k, v := range d.out {
			out[k] = v
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	// Every registered delivery is completed, even if a callback or the
	// run panics, so duplicates never wait on an abandoned entry.
	finished := false
	defer func() {
		if !finished {
			h.finish(id, d, nil)
		}
	}()
	// The run is not tied to this connection: a sender that times out and
	// retries gets the same result instead of a second run.
	ctx := context.WithoutCancel(r.Context())
	res, err := h.cfg.Agent.Run(ctx, h.cfg.Prompt(body), kritrim.Thread(h.cfg.Thread(body)))
	if err != nil {
		if h.cfg.Logger != nil {
			h.cfg.Logger("webhook run: " + err.Error())
		}
		finished = true
		h.finish(id, d, nil)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "run failed"})
		return
	}
	out := map[string]any{"text": res.Text, "thread": res.Thread, "stop": res.Stop}
	finished = true
	h.finish(id, d, out)
	writeJSON(w, http.StatusOK, out)
}

func (h *webhook) fresh(ts string) bool {
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	d := time.Since(time.Unix(n, 0))
	return d <= h.cfg.Tolerance && d >= -h.cfg.Tolerance
}

// begin registers a delivery or returns the existing one. A key reused for a
// different body is a conflict, not a duplicate.
func (h *webhook) begin(id, print string) (*delivery, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if d, ok := h.seen[id]; ok && (now.Before(d.expires) || !isDone(d)) {
		if d.print != print {
			return nil, false, errors.New("idempotency key reused for a different payload")
		}
		return d, false, nil
	}
	if len(h.seen) >= h.cfg.MaxEntries {
		h.evict(now)
		if len(h.seen) >= h.cfg.MaxEntries {
			return nil, false, errBusy
		}
	}
	d := &delivery{done: make(chan struct{}), print: print, expires: now.Add(h.cfg.DedupeTTL)}
	h.seen[id] = d
	return d, true, nil
}

func (h *webhook) finish(id string, d *delivery, out map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d.out, d.failed = out, out == nil
	// A failed run may be retried by the sender. Only remove the entry this
	// leader owns.
	if d.failed && h.seen[id] == d {
		delete(h.seen, id)
	}
	close(d.done)
}

// evict drops deliveries that are finished and past their TTL. Unexpired
// entries are kept because their signatures may still be replayed, and
// in-flight entries are kept so their duplicates never run twice.
func (h *webhook) evict(now time.Time) {
	for k, d := range h.seen {
		if now.After(d.expires) && isDone(d) {
			delete(h.seen, k)
		}
	}
}

func isDone(d *delivery) bool {
	select {
	case <-d.done:
		return true
	default:
		return false
	}
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
