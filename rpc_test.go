package kritrim

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// An oversized guest frame must end the peer instead of growing memory.
func TestPeerClosesOnOversizedFrame(t *testing.T) {
	pr, pw := io.Pipe()
	p := newPeer(pr, io.Discard)
	line := `{"method":"x","params":"` + strings.Repeat("a", maxFrame) + `"}` + "\n"
	go func() {
		_, _ = io.WriteString(pw, line)
		_ = pw.Close()
	}()
	select {
	case <-p.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("peer did not shut down on an oversized frame")
	}
	_ = pr.Close()
}

// A result that is well under the frame cap in raw bytes but expands under
// JSON escaping (control bytes become \u00XX) must still be delivered.
func TestPeerDeliversLargeEscapedFrame(t *testing.T) {
	pr, pw := io.Pipe()
	p := newPeer(pr, io.Discard)
	got := make(chan int, 1)
	p.handle("echo", func(_ context.Context, params json.RawMessage) (any, error) {
		var s string
		if err := json.Unmarshal(params, &s); err != nil {
			return nil, err
		}
		got <- len(s)
		return nil, nil
	})
	payload := strings.Repeat("\x01", 8<<20) // 8 MiB raw, 48 MiB escaped
	params, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := json.Marshal(wire{Method: "echo", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame)+1 > maxFrame {
		t.Fatalf("test frame %d bytes exceeds maxFrame %d", len(frame)+1, maxFrame)
	}
	go func() {
		_, _ = pw.Write(append(frame, '\n'))
	}()
	select {
	case n := <-got:
		if n != len(payload) {
			t.Fatalf("handler got %d bytes, want %d", n, len(payload))
		}
	case <-p.Done():
		t.Fatal("peer closed on a frame under maxFrame")
	case <-time.After(20 * time.Second):
		t.Fatal("handler not invoked")
	}
	_ = pw.Close()
	_ = pr.Close()
}

// Guest-initiated handlers are capped. The request past the cap must wait
// until an earlier handler finishes.
func TestPeerCapsInFlightHandlers(t *testing.T) {
	pr, pw := io.Pipe()
	p := newPeer(pr, io.Discard)
	release := make(chan struct{})
	var running int64
	p.handle("block", func(_ context.Context, _ json.RawMessage) (any, error) {
		atomic.AddInt64(&running, 1)
		<-release
		return nil, nil
	})
	line := []byte(`{"id":1,"method":"block"}` + "\n")
	go func() {
		for i := 0; i < maxInFlight+1; i++ {
			if _, err := pw.Write(line); err != nil {
				return
			}
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for atomic.LoadInt64(&running) < maxInFlight {
		if time.Now().After(deadline) {
			t.Fatal("handlers did not start")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := atomic.LoadInt64(&running); n != maxInFlight {
		t.Fatalf("%d handlers running, want cap %d", n, maxInFlight)
	}
	close(release)
	deadline = time.Now().Add(10 * time.Second)
	for atomic.LoadInt64(&running) < maxInFlight+1 {
		if time.Now().After(deadline) {
			t.Fatal("request past the cap never ran after a slot freed")
		}
		time.Sleep(time.Millisecond)
	}
	_ = pw.Close()
	_ = pr.Close()
}
