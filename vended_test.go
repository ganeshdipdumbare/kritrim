package kritrim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEditFileTool(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := testEnv(t)
	tool := EditFile(EditConfig{Root: dir})
	if _, err := tool.run(ctx, map[string]any{"path": "a.txt", "old": "world", "new": "there"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "hello there" {
		t.Fatalf("%q", b)
	}
	if _, err := tool.run(ctx, map[string]any{"path": "a.txt", "old": "zzz", "new": "x"}); err == nil {
		t.Fatal("missing old text should fail")
	}
}

func TestReadLineNumbers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("one\ntwo\nthree"), 0o600)
	out, err := Read(ReadConfig{Root: dir}).run(testEnv(t), map[string]any{"path": "a.txt", "offset": 2, "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2\ttwo") || strings.Contains(out, "three") {
		t.Fatalf("%q", out)
	}
}

func TestStopToolEndsRun(t *testing.T) {
	m := &spyModel{turns: []Turn{TurnTool("stop", map[string]any{"reason": "done"}), {Text: "should not run"}}}
	a := newAgent(t, Config{Model: m, Tools: []Tool{Stop()}})
	res, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != "stopped" || res.Text == "should not run" {
		t.Fatalf("%+v", res)
	}
}

func TestNotebookPersistsAcrossRuns(t *testing.T) {
	m := &spyModel{turns: []Turn{
		TurnTool("notebook", map[string]any{"op": "create", "name": "n", "content": "a"}),
		{Text: "ok"},
		TurnTool("notebook", map[string]any{"op": "append", "name": "n", "content": "b"}),
		{Text: "ok"},
		TurnTool("notebook", map[string]any{"op": "read", "name": "n"}),
		{Text: "done"},
	}}
	a := newAgent(t, Config{Model: m, Tools: []Tool{Notebook()}})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := a.Run(ctx, "x", Thread("t")); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := a.Session(ctx, "t")
	if err != nil || sess.Values["notebook:n"] != "a\nb" {
		t.Fatalf("%+v %v", sess, err)
	}
}

func TestAskUserTool(t *testing.T) {
	m := &spyModel{turns: []Turn{TurnTool("ask_user", map[string]any{"question": "ok?"}), {Text: "done"}}}
	a := newAgent(t, Config{Model: m, Tools: []Tool{AskUser()},
		Approve: func(ctx context.Context, req Approval) (Decision, error) {
			if req.Args["question"] != "ok?" {
				t.Fatalf("question %+v", req.Args)
			}
			return Respond("yes"), nil
		}})
	res, err := a.Run(context.Background(), "ask")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Steps[0].ToolResults[0].Content; got != "yes" {
		t.Fatalf("%q", got)
	}
}

func TestSleepTool(t *testing.T) {
	start := now()
	if _, err := Sleep(2*time.Second).run(testEnv(t), map[string]any{"seconds": 0}); err != nil {
		t.Fatal(err)
	}
	if now().Sub(start) > 2*time.Second {
		t.Fatal("sleep ignored the bound")
	}
}
