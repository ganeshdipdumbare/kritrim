package trigger

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

type counterModel struct{ n atomic.Int32 }

func (m *counterModel) Generate(ctx context.Context, req kritrim.Request) (*kritrim.Response, error) {
	m.n.Add(1)
	return &kritrim.Response{Text: "tick", Stop: "stop"}, nil
}

func (m *counterModel) Stream(ctx context.Context, req kritrim.Request, emit func(kritrim.Delta)) (*kritrim.Response, error) {
	return m.Generate(ctx, req)
}

type slowModel struct {
	delay time.Duration
	n     atomic.Int32
}

func (m *slowModel) Generate(ctx context.Context, req kritrim.Request) (*kritrim.Response, error) {
	m.n.Add(1)
	time.Sleep(m.delay)
	return &kritrim.Response{Text: "slow", Stop: "stop"}, nil
}

func (m *slowModel) Stream(ctx context.Context, req kritrim.Request, emit func(kritrim.Delta)) (*kritrim.Response, error) {
	return m.Generate(ctx, req)
}

func TestScheduler(t *testing.T) {
	m := &counterModel{}
	a, err := kritrim.New(kritrim.Config{Model: m})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s, err := NewScheduler(a, Schedule{Every: 10 * time.Millisecond, Thread: "tick", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	s.Start(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for m.n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	s.Close()
	if m.n.Load() < 2 {
		t.Fatalf("runs %d", m.n.Load())
	}
}

func TestWebhook(t *testing.T) {
	m := &counterModel{}
	a, err := kritrim.New(kritrim.Config{Model: m})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h, err := Webhook(WebhookConfig{Agent: a, Secret: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"prompt":"hi"}`)
	now := time.Now()
	sendAt := func(ts time.Time, sig string, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set(SignatureHeader, sig)
		req.Header.Set(TimestampHeader, strconv.FormatInt(ts.Unix(), 10))
		if key != "" {
			req.Header.Set(IdempotencyHeader, key)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	send := func(sig, key string) *httptest.ResponseRecorder { return sendAt(now, sig, key) }
	sig := Sign("s3cret", now, "k1", body)
	if rr := send("bad", "1"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature status %d", rr.Code)
	}
	// The key is covered by the MAC: a captured signature cannot be
	// replayed under a fresh Idempotency-Key.
	if rr := send(sig, "k2"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("re-keyed replay status %d", rr.Code)
	}
	// A stale timestamp is refused even with a valid MAC.
	old := now.Add(-time.Hour)
	if rr := sendAt(old, Sign("s3cret", old, "k1", body), "k1"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("stale status %d", rr.Code)
	}
	rr := send(sig, "k1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var out map[string]any
	json.Unmarshal(rr.Body.Bytes(), &out)
	if out["text"] != "tick" {
		t.Fatalf("%v", out)
	}
	before := m.n.Load()
	rr2 := send(sig, "k1")
	json.Unmarshal(rr2.Body.Bytes(), &out)
	if out["duplicate"] != true || m.n.Load() != before {
		t.Fatalf("duplicate %v runs %d", out, m.n.Load()-before)
	}
	// Without a key, an exact replay is deduplicated by its signature.
	sig2 := Sign("s3cret", now, "", body)
	send(sig2, "")
	before = m.n.Load()
	out = nil
	json.Unmarshal(send(sig2, "").Body.Bytes(), &out)
	if out["duplicate"] != true || m.n.Load() != before {
		t.Fatalf("keyless replay %v runs %d", out, m.n.Load()-before)
	}
}

func TestWebhookConcurrentDuplicates(t *testing.T) {
	m := &slowModel{delay: 100 * time.Millisecond}
	a, err := kritrim.New(kritrim.Config{Model: m})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h, _ := Webhook(WebhookConfig{Agent: a, Secret: "s3cret"})
	body := []byte(`{"prompt":"hi"}`)
	now := time.Now()
	sig := Sign("s3cret", now, "dup", body)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			req.Header.Set(SignatureHeader, sig)
			req.Header.Set(TimestampHeader, strconv.FormatInt(now.Unix(), 10))
			req.Header.Set(IdempotencyHeader, "dup")
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()
	if n := m.n.Load(); n != 1 {
		t.Fatalf("model calls %d, want 1", n)
	}
}
