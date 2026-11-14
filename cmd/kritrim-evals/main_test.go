package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/trace"
)

// TestMain lets the test binary act as the agent program under test.
func TestMain(m *testing.M) {
	switch os.Getenv("KRITRIM_EVALS_AGENT") {
	case "text":
		in, _ := io.ReadAll(os.Stdin)
		fmt.Printf("You asked: %s", in)
		os.Exit(0)
	case "json":
		in, _ := io.ReadAll(os.Stdin)
		_ = json.NewEncoder(os.Stdout).Encode(kritrim.Result{Text: "Paris", Stop: kritrim.StopDone,
			Steps: []kritrim.Step{{ToolCalls: []kritrim.ToolCall{{Name: "lookup", Args: map[string]any{"q": string(in)}}}}}})
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(3)
	}
	os.Exit(m.Run())
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func cli(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func TestRunCommandPerCase(t *testing.T) {
	cases := writeFile(t, "cases.jsonl", `{"name":"echo","input":"capital of France","expected":"France"}
{"name":"miss","input":"hello","expected":"Paris"}
`)
	report := filepath.Join(t.TempDir(), "report.json")
	t.Setenv("KRITRIM_EVALS_AGENT", "text")
	code, out, errs := cli(t, "run", "-cases", cases, "-contains", "-o", report, "--", os.Args[0])
	if code != 1 || !strings.Contains(out, "PASS  echo") || !strings.Contains(out, "FAIL  miss") {
		t.Fatalf("code=%d out=%s err=%s", code, out, errs)
	}
	if code, _, _ := cli(t, "report", report); code != 1 {
		t.Fatalf("report exit %d", code)
	}
}

func TestRunJSONResultKeepsTools(t *testing.T) {
	cases := writeFile(t, "cases.json", `[{"input":"capital of France","expected":"Paris"}]`)
	t.Setenv("KRITRIM_EVALS_AGENT", "json")
	code, out, errs := cli(t, "run", "-cases", cases, "-exact", "-detect", "--", os.Args[0])
	if code != 0 || !strings.Contains(out, "PASS  case-1") {
		t.Fatalf("code=%d out=%s err=%s", code, out, errs)
	}
}

func TestRunCommandFailure(t *testing.T) {
	cases := writeFile(t, "cases.jsonl", `{"name":"x","input":"y"}`)
	t.Setenv("KRITRIM_EVALS_AGENT", "fail")
	code, out, _ := cli(t, "run", "-cases", cases, "--", os.Args[0])
	if code != 1 || !strings.Contains(out, "boom") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestTraces(t *testing.T) {
	var buf bytes.Buffer
	a, _ := kritrim.New(kritrim.Config{Model: kritrim.Mock(kritrim.Turn{Text: "Paris"}), Hooks: trace.JSON(&buf, trace.Content())})
	defer a.Close()
	if _, err := a.Run(context.Background(), "capital of France?", kritrim.Thread("t")); err != nil {
		t.Fatal(err)
	}
	path := writeFile(t, "trace.jsonl", buf.String())
	code, out, errs := cli(t, "traces", "-regex", "^Paris$", "-detect", path)
	if code != 0 || !strings.Contains(out, "1 passed") {
		t.Fatalf("code=%d out=%s err=%s", code, out, errs)
	}
	if code, _, _ := cli(t, "traces", "-regex", "^Rome$", path); code != 1 {
		t.Fatalf("failing trace exit %d", code)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"bogus"},
		{"run", "-cases", "x.jsonl"},
		{"generate"},
		{"generate", "-model", "nope"},
		{"traces", "-judge", "faithful", "x"},
	} {
		if code, _, _ := cli(t, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if _, err := parseModel("acme:x"); err == nil {
		t.Error("unknown provider accepted")
	}
	if m, err := parseModel("anthropic:claude-sonnet-5-5"); err != nil || m == nil {
		t.Errorf("anthropic: %v", err)
	}
}
