package authz_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/authz"
)

func policySet(t *testing.T) *authz.Set {
	t.Helper()
	s, err := authz.New(
		authz.Permit("*").For("admin"),
		authz.Permit("current_time", "web_*"),
		authz.Permit("refund").For("support").
			When(func(r authz.Request) bool { return r.Number("amount") <= 100 }).
			Because("support may refund up to 100"),
		authz.Forbid("shell").Because("nobody runs commands here"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCheck(t *testing.T) {
	s := policySet(t)
	cases := []struct {
		name  string
		req   authz.Request
		allow bool
		why   string
	}{
		{"permitted to everyone", authz.Request{Tool: "current_time"}, true, ""},
		{"glob", authz.Request{Tool: "web_fetch"}, true, ""},
		{"role gate blocks", authz.Request{Tool: "refund", Args: map[string]any{"amount": 10.0}}, false, "not permitted"},
		{"role gate passes", authz.Request{Tool: "refund", Roles: []string{"support"},
			Args: map[string]any{"amount": 10.0}}, true, ""},
		{"condition blocks", authz.Request{Tool: "refund", Roles: []string{"support"},
			Args: map[string]any{"amount": 500.0}}, false, "not permitted"},
		{"admin wildcard", authz.Request{Tool: "refund", Roles: []string{"admin"},
			Args: map[string]any{"amount": 9999.0}}, true, ""},
		// Explicit forbid beats the admin wildcard that would permit it.
		{"forbid wins", authz.Request{Tool: "shell", Roles: []string{"admin"}}, false, "nobody runs commands here"},
		// Nothing matches an unknown tool: default deny.
		{"default deny", authz.Request{Tool: "wire_transfer", Roles: []string{"support"}}, false, "not permitted"},
	}
	for _, tc := range cases {
		err := s.Check(tc.req)
		if tc.allow && err != nil {
			t.Errorf("%s: denied: %v", tc.name, err)
			continue
		}
		if !tc.allow {
			if err == nil {
				t.Errorf("%s: allowed", tc.name)
				continue
			}
			if !errors.Is(err, authz.ErrDenied) {
				t.Errorf("%s: %v does not wrap ErrDenied", tc.name, err)
			}
			if !strings.Contains(err.Error(), tc.why) {
				t.Errorf("%s: %q does not explain %q", tc.name, err, tc.why)
			}
		}
	}
}

// TestDenialExplainsTheRule: a call that fell outside a ceiling must come
// back with the ceiling, because the model repeats that sentence to the
// user.
func TestDenialExplainsTheRule(t *testing.T) {
	s := policySet(t)
	err := s.Check(authz.Request{Tool: "refund", Roles: []string{"support"},
		Args: map[string]any{"amount": 4000.0}})
	if err == nil {
		t.Fatal("allowed")
	}
	if !strings.Contains(err.Error(), "support may refund up to 100") {
		t.Fatalf("denial does not explain the rule: %v", err)
	}
	// A tool no policy mentions has nothing to explain, and the message
	// stays short rather than listing unrelated rules.
	err = s.Check(authz.Request{Tool: "wire_transfer", Roles: []string{"support"}})
	if strings.Contains(err.Error(), "refund") {
		t.Fatalf("unrelated rules leaked into the denial: %v", err)
	}
}

func TestByUser(t *testing.T) {
	s, err := authz.New(authz.Permit("deploy").By("u-7", "ops-*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"u-7", "ops-ana"} {
		if err := s.Check(authz.Request{Tool: "deploy", User: user}); err != nil {
			t.Errorf("%s denied: %v", user, err)
		}
	}
	if err := s.Check(authz.Request{Tool: "deploy", User: "u-8"}); err == nil {
		t.Error("u-8 allowed")
	}
}

func TestNewRejectsBadPolicies(t *testing.T) {
	if _, err := authz.New(); err == nil {
		t.Error("empty set accepted")
	}
	if _, err := authz.New(authz.Permit()); err == nil {
		t.Error("policy with no action accepted")
	}
	if _, err := authz.New(authz.Permit(" ")); err == nil {
		t.Error("empty action accepted")
	}
	if _, err := authz.New(authz.Permit("[")); err == nil {
		t.Error("broken pattern accepted")
	}
}

// TestPanickingConditionDenies: a condition that panics must deny, not take
// the run down.
func TestPanickingConditionDenies(t *testing.T) {
	s, err := authz.New(authz.Permit("pay").When(func(r authz.Request) bool {
		return r.Args["nested"].(map[string]any)["x"] == "y" // nil map assertion
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(authz.Request{Tool: "pay"}); err == nil {
		t.Fatal("a panicking condition allowed the call")
	}
}

// TestHooksDenyTheCall runs a real agent: the model asks for a tool policy
// forbids, the tool never runs, and the model is told why.
func TestHooksDenyTheCall(t *testing.T) {
	ran := false
	refund := kritrim.NewTool("refund", "refund an order", func(ctx context.Context, in struct {
		Amount float64 `json:"amount"`
	}) (string, error) {
		ran = true
		return "refunded", nil
	})
	s := policySet(t)
	a, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(
			kritrim.TurnTool("refund", map[string]any{"amount": 500.0}),
			kritrim.Turn{Text: "I cannot refund that much."},
		),
		Tools: []kritrim.Tool{refund},
		Hooks: s.Hooks(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	res, err := a.Run(context.Background(), "refund 500", kritrim.Roles("support"))
	if err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Fatal("the tool ran despite the policy")
	}
	result := res.Steps[0].ToolResults[0]
	if !result.IsError || !strings.Contains(result.Content, "not permitted") {
		t.Fatalf("tool result = %+v", result)
	}
	// The run completed: a denial is a message to the model, not a failure.
	if res.Stop != "stop" || res.Text == "" {
		t.Fatalf("run = %+v", res)
	}
}

// TestHooksAllowWithinLimits is the same policy and the same tool, inside
// the ceiling.
func TestHooksAllowWithinLimits(t *testing.T) {
	ran := false
	refund := kritrim.NewTool("refund", "refund an order", func(ctx context.Context, in struct {
		Amount float64 `json:"amount"`
	}) (string, error) {
		ran = true
		return "refunded", nil
	})
	a, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(
			kritrim.TurnTool("refund", map[string]any{"amount": 50.0}),
			kritrim.Turn{Text: "Refunded."},
		),
		Tools: []kritrim.Tool{refund},
		Hooks: policySet(t).Hooks(),
		Roles: []string{"support"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Run(context.Background(), "refund 50"); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("the tool did not run")
	}
}

// TestHooksSeeTheIdentityOfTheRun checks the policy reads the per-run
// identity, not the agent's defaults, so one agent can serve many callers.
func TestHooksSeeTheIdentityOfTheRun(t *testing.T) {
	var seen []string
	s, err := authz.New(authz.Permit("current_time").When(func(r authz.Request) bool {
		seen = append(seen, r.User+"/"+strings.Join(r.Roles, ",")+"/"+r.Thread)
		return r.HasRole("admin")
	}))
	if err != nil {
		t.Fatal(err)
	}
	newAgent := func() *kritrim.Agent {
		a, err := kritrim.New(kritrim.Config{
			Model: kritrim.Mock(kritrim.TurnTool("current_time", map[string]any{}), kritrim.Turn{Text: "ok"}),
			Tools: []kritrim.Tool{kritrim.Time()},
			Hooks: s.Hooks(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := newAgent()
	res, err := a.Run(context.Background(), "time?", kritrim.User("u-1"), kritrim.Roles("admin"), kritrim.Thread("t-1"))
	a.Close()
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps[0].ToolResults[0].IsError {
		t.Fatalf("admin denied: %+v", res.Steps[0].ToolResults[0])
	}
	b := newAgent()
	res, err = b.Run(context.Background(), "time?", kritrim.User("u-2"), kritrim.Roles("guest"), kritrim.Thread("t-2"))
	b.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Steps[0].ToolResults[0].IsError {
		t.Fatal("guest allowed")
	}
	want := []string{"u-1/admin/t-1", "u-2/guest/t-2"}
	if len(seen) != 2 || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("policy saw %v, want %v", seen, want)
	}
}
