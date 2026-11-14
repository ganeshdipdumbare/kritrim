package kritrim

import (
	"context"
	"io"
	"path/filepath"
	"strconv"
	"testing"
)

// The benchmarks measure kritrim's own overhead with the offline Mock
// model: what a run costs before any provider latency.

func benchAgent(b *testing.B, cfg Config) *Agent {
	b.Helper()
	a, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { a.Close() })
	return a
}

func BenchmarkRun(b *testing.B) {
	a := benchAgent(b, Config{Model: Mock(Turn{Text: "hi"})})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := a.Run(ctx, "hello"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRunFiveTools(b *testing.B) {
	var calls []ToolCall
	for i := range 5 {
		calls = append(calls, ToolCall{ID: "c" + strconv.Itoa(i), Name: "echo", Args: map[string]any{"text": "x"}})
	}
	tool := NewTool("echo", "echo", func(ctx context.Context, in struct {
		Text string `json:"text"`
	}) (string, error) {
		return in.Text, nil
	})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		a, _ := New(Config{Model: Mock(Turn{ToolCalls: calls}, Turn{Text: "done"}), Tools: []Tool{tool}})
		b.StartTimer()
		if _, err := a.Run(ctx, "go"); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		a.Close()
		b.StartTimer()
	}
}

func BenchmarkStream(b *testing.B) {
	a := benchAgent(b, Config{Model: Mock(Turn{Text: "hi"})})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		for _, err := range a.Stream(ctx, "hello") {
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkRunWithSession(b *testing.B) {
	a := benchAgent(b, Config{Model: Mock(Turn{Text: "hi"}), Session: FileSessions(b.TempDir())})
	ctx := context.Background()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		i++
		if _, err := a.Run(ctx, "hello", Thread("t"+strconv.Itoa(i))); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCheckArgs(b *testing.B) {
	tool := NewTool("order", "look up", func(ctx context.Context, in struct {
		ID     string   `json:"id" desc:"order id"`
		Status string   `json:"status" enum:"open,closed"`
		Tags   []string `json:"tags"`
		Limit  int      `json:"limit"`
	}) (string, error) {
		return "", nil
	})
	args := map[string]any{"id": "A-17", "status": "open", "tags": []any{"a", "b"}, "limit": float64(5)}
	b.ReportAllocs()
	for b.Loop() {
		if err := tool.checkArgs(args); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAuditWrite(b *testing.B) {
	for _, keyed := range []bool{false, true} {
		name := "sha256"
		var opts []AuditOption
		if keyed {
			name = "hmac"
			opts = append(opts, AuditKey([]byte("0123456789abcdef0123456789abcdef")))
		}
		b.Run(name, func(b *testing.B) {
			sink := AuditFile(filepath.Join(b.TempDir(), "audit.jsonl"), opts...)
			ctx := context.Background()
			e := Entry{Kind: "tool", Name: "echo", Detail: "args={} result=ok"}
			b.ReportAllocs()
			for b.Loop() {
				if err := sink.Write(ctx, e); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkVerifyAudit1000(b *testing.B) {
	path := filepath.Join(b.TempDir(), "audit.jsonl")
	sink := AuditFile(path)
	for range 1000 {
		_ = sink.Write(context.Background(), Entry{Kind: "tool", Name: "echo"})
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := VerifyAudit(path); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSignedSessionRoundTrip(b *testing.B) {
	store := SignedSessions(MemorySessions(), []byte("0123456789abcdef0123456789abcdef"))
	ctx := context.Background()
	msgs := make([]Message, 20)
	for i := range msgs {
		msgs[i] = Message{Role: RoleUser, Content: "message number " + strconv.Itoa(i)}
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		i++
		id := "t" + strconv.Itoa(i)
		if err := store.Save(ctx, &Session{ID: id, Messages: msgs}); err != nil {
			b.Fatal(err)
		}
		if _, err := store.Load(ctx, id); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEstimateTokens(b *testing.B) {
	msgs := make([]Message, 100)
	for i := range msgs {
		msgs[i] = Message{Role: RoleAssistant, Content: "a reasonably long assistant message that goes on for a while"}
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = EstimateTokens(msgs)
	}
}

func BenchmarkTraceOverhead(b *testing.B) {
	a := benchAgent(b, Config{Model: Mock(Turn{Text: "hi"}), Hooks: Hooks{
		AfterModel: func(ctx context.Context, e *ModelEvent) { _, _ = io.Discard.Write([]byte(e.Response.Text)) },
	}})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := a.Run(ctx, "hello"); err != nil {
			b.Fatal(err)
		}
	}
}
