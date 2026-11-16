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
	// BindPath makes the signature cover the request path (see SignPath). Off
	// by default, so existing senders keep working; turn it on when one secret
	// is shared by several endpoints.
	BindPath bool
	// RunTimeout bounds one delivery's agent run. A run that hangs past it
	// fails and releases its claim, so duplicates are not held forever.
	// Default 10 minutes.
	RunTimeout time.Duration
	// DedupeTTL is how long a delivery is remembered. Default 24h; it is
	// never shorter than twice Tolerance.
	DedupeTTL time.Duration
	// Store remembers deliveries. Default MemoryDedupe(0), one process;
	// share a SQLDedupe when several instances serve the webhook. A full
	// store answers 503 with Retry-After.
	Store DedupeStore
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

// SignPath is Sign for an endpoint configured with BindPath. The MAC also
// covers the request path, so a delivery signed for one endpoint is refused
// by another that shares the secret.
func SignPath(secret string, ts time.Time, key, path string, body []byte) string {
	return signPath([]byte(secret), strconv.FormatInt(ts.Unix(), 10), key, path, body)
}

func signPath(secret []byte, ts, key, path string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "kritrim-webhook-v2\n%d:%s\n%d:%s\n%d:%s\n%d:", len(ts), ts, len(key), key, len(path), path, len(body))
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
	if cfg.RunTimeout <= 0 {
		cfg.RunTimeout = 10 * time.Minute
	}
	if cfg.DedupeTTL <= 0 {
		cfg.DedupeTTL = 24 * time.Hour
	}
	// A claim must outlive the run it guards. Shorter, and it can expire
	// mid-run and let a retry run the delivery a second time.
	if floor := cfg.RunTimeout + 2*cfg.Tolerance; cfg.DedupeTTL < floor {
		cfg.DedupeTTL = floor
	}
	if cfg.Store == nil {
		cfg.Store = MemoryDedupe(0)
	}
	if cfg.Thread == nil {
		cfg.Thread = func([]byte) string { return "webhook" }
	}
	if cfg.Prompt == nil {
		cfg.Prompt = defaultPrompt
	}
	h := &webhook{cfg: cfg, secret: []byte(secret), waiting: map[string]chan struct{}{}}
	return h, nil
}

type webhook struct {
	cfg    WebhookConfig
	secret []byte

	// waiting lets duplicates in this process wait for the leader's
	// result instead of polling the store.
	mu      sync.Mutex
	waiting map[string]chan struct{}
}

func (h *webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		// Only an over-size body is a 413. A failed read is the sender's
		// problem or the network's, and is not permanent for the payload.
		var big *http.MaxBytesError
		if errors.As(err, &big) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "could not read body", http.StatusBadRequest)
		return
	}
	ts := r.Header.Get(TimestampHeader)
	key := r.Header.Get(IdempotencyHeader)
	sig := r.Header.Get(SignatureHeader)
	want := sign(h.secret, ts, key, body)
	if h.cfg.BindPath {
		want = signPath(h.secret, ts, key, r.URL.Path, body)
	}
	if !h.fresh(ts) || !hmac.Equal([]byte(sig), []byte(want)) {
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
	print := hex.EncodeToString(sum[:])
	for {
		expires := time.Now().Add(h.cfg.DedupeTTL)
		c, err := h.cfg.Store.Claim(r.Context(), id, print, expires)
		switch {
		case errors.Is(err, ErrBusy):
			w.Header().Set("Retry-After", "5")
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		case errors.Is(err, ErrConflict):
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		case err != nil:
			h.logf("webhook dedupe: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "dedupe failed"})
			return
		case c.Done:
			out := map[string]any{}
			_ = json.Unmarshal(c.Result, &out)
			out["duplicate"] = true
			writeJSON(w, http.StatusOK, out)
			return
		case c.Won:
			h.run(w, r, id, body, expires)
			return
		}
		// Running elsewhere: wait for it, then look again. A leader
		// that fails releases the delivery and this caller runs it.
		// Waiting is bounded: a delivery that never finishes must not hold
		// its duplicates forever.
		wctx, cancel := context.WithTimeout(r.Context(), h.cfg.RunTimeout)
		ok := h.wait(wctx, id)
		cancel()
		if !ok {
			if r.Context().Err() == nil {
				w.Header().Set("Retry-After", "5")
				http.Error(w, "busy", http.StatusServiceUnavailable)
			}
			return
		}
	}
}

// run executes a delivery this handler won. Every claim is completed or
// released, even if a callback or the run panics, so duplicates never wait
// on an abandoned delivery.
func (h *webhook) run(w http.ResponseWriter, r *http.Request, id string, body []byte, expires time.Time) {
	done := make(chan struct{})
	h.mu.Lock()
	h.waiting[id] = done
	h.mu.Unlock()
	// The run is not tied to this connection: a sender that times out and
	// retries gets the same result instead of a second run.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), h.cfg.RunTimeout)
	defer cancel()
	// Release and Complete act only on this leader's claim (see withFence).
	// They run on a context that is not canceled: a run that hit RunTimeout
	// must still release its claim, or the delivery stays held.
	fctx := withFence(context.WithoutCancel(ctx), expires)
	finished := false
	defer func() {
		if !finished {
			_ = h.cfg.Store.Release(fctx, id)
		}
		h.mu.Lock()
		delete(h.waiting, id)
		h.mu.Unlock()
		close(done)
	}()
	res, err := h.cfg.Agent.Run(ctx, h.cfg.Prompt(body), kritrim.Thread(h.cfg.Thread(body)))
	if err != nil {
		h.logf("webhook run: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "run failed"})
		return
	}
	out := map[string]any{"text": res.Text, "thread": res.Thread, "stop": res.Stop}
	result, _ := json.Marshal(out)
	if err := h.cfg.Store.Complete(fctx, id, result); err != nil {
		h.logf("webhook dedupe: %v", err)
	}
	finished = true
	writeJSON(w, http.StatusOK, out)
}

// wait blocks until a delivery running in this process finishes, or for a
// short poll interval when it runs in another one. It reports false when
// the caller went away.
func (h *webhook) wait(ctx context.Context, id string) bool {
	h.mu.Lock()
	ch := h.waiting[id]
	h.mu.Unlock()
	var tick <-chan time.Time
	if ch == nil {
		t := time.NewTimer(250 * time.Millisecond)
		defer t.Stop()
		tick = t.C
	}
	select {
	case <-ch:
	case <-tick:
	case <-ctx.Done():
		return false
	}
	return true
}

func (h *webhook) logf(format string, args ...any) {
	if h.cfg.Logger != nil {
		h.cfg.Logger(fmt.Sprintf(format, args...))
	}
}

func (h *webhook) fresh(ts string) bool {
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	d := time.Since(time.Unix(n, 0))
	return d <= h.cfg.Tolerance && d >= -h.cfg.Tolerance
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
