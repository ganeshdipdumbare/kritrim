package trigger

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	send := func(sig string, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("X-Kritrim-Signature", sig)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if rr := send("bad", "1"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature status %d", rr.Code)
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
}
