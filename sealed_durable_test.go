package kritrim

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSealedDurableApproval(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not found")
	}
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "box")
	build := exec.Command("go", "build", "-o", bin, "./internal/boxtest")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build box: %v: %s", err, out)
	}
	sessions := FileSessions(filepath.Join(dir, "sessions"))

	refund := Risky(NewTool("refund", "refund", func(ctx context.Context, in struct {
		Order string `json:"order"`
	}) (string, error) {
		return "refunded " + in.Order, nil
	}))
	sealed := &Sealed{Sandbox: Local(), Binary: bin, AllowUnsafeLocal: true}

	a1, err := New(Config{
		Model:   Mock(TurnTool("refund", map[string]any{"order": "A-17"}), Turn{Text: "done"}),
		Tools:   []Tool{refund},
		Session: sessions,
		Sealed:  sealed,
		Approve: func(ctx context.Context, req Approval) (Decision, error) { return Pause(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a1.Run(context.Background(), "refund A-17", Thread("t1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != "interrupt" || len(res.Interrupts) != 1 {
		t.Fatalf("%+v", res)
	}
	_ = a1.Close()

	a2, err := New(Config{
		Model:   Mock(Turn{Text: "done"}),
		Tools:   []Tool{refund},
		Session: sessions,
		Sealed:  sealed,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	res2, err := a2.Run(context.Background(), "", Thread("t1"),
		Decisions(map[string]Decision{res.Interrupts[0].ID: Approve()}))
	if err != nil {
		t.Fatal(err)
	}
	if res2.Stop != "stop" || res2.Text != "done" {
		t.Fatalf("%+v", res2)
	}
	if got := res2.Steps[0].ToolResults[0].Content; got != "refunded A-17" {
		t.Fatalf("tool result %q", got)
	}
}
