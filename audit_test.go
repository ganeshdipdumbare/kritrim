package kritrim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink := AuditFile(path)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := sink.Write(ctx, Entry{Kind: "tool", Name: "x", Detail: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := VerifyAudit(path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"prev":""`) {
		t.Fatal("first entry should have empty prev")
	}
}

func TestAuditTamper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink := AuditFile(path)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = sink.Write(ctx, Entry{Kind: "tool", Name: "x"})
	}
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	lines[1] = strings.Replace(lines[1], `"name":"x"`, `"name":"y"`, 1)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAudit(path); err == nil {
		t.Fatal("tampered audit verified")
	}
}

func TestRunWritesAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAgent(t, Config{Model: Mock(Turn{Text: "hi"}), Audit: AuditFile(path)})
	if _, err := a.Run(context.Background(), "x", User("u1"), Thread("t1")); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAudit(path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"user":"u1"`) || !strings.Contains(string(b), `"thread":"t1"`) {
		t.Fatalf("identity missing: %s", b)
	}
}
