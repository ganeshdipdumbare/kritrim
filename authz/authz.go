// Package authz authorizes tool calls against policy.
//
// kritrim's own checks answer "may this agent do this at all": an allowlist
// for Shell, a root for Files, an approval for a risky tool. They are per
// agent and fixed at construction. This package answers the other
// question — "may this caller do this, now, with these arguments" — and
// answers it per run, from the identity the run carries.
//
//	policy, err := authz.New(
//		authz.Permit("*").For("admin"),
//		authz.Permit("current_time", "web_fetch"),
//		authz.Permit("refund").For("support").
//			When(func(r authz.Request) bool { return r.Number("amount") <= 100 }).
//			Because("support may refund up to 100"),
//		authz.Forbid("shell"),
//	)
//	a, _ := kritrim.New(kritrim.Config{Model: model, Tools: tools, Hooks: policy.Hooks()})
//	a.Run(ctx, prompt, kritrim.User("u-7"), kritrim.Roles("support"))
//
// The rules are Cedar's, and there are only three:
//
//  1. An explicit Forbid wins over every Permit.
//  2. Otherwise one matching Permit is enough.
//  3. With nothing matching, the call is denied.
//
// Default deny is the point. A tool nobody was permitted is not callable,
// so adding a tool to an agent does not quietly widen what every caller
// can reach, and a typo in a policy fails closed.
//
// A denied call is not an error: the model is told the call was blocked and
// keeps going, usually by explaining the limit to the user or trying
// something else. Write Because for the model to read — it is the sentence
// the user ends up hearing. A call denied because nothing permitted it is
// told the Because text of every policy that covers the tool, so a caller
// who went over a ceiling learns what the ceiling was.
//
// Policy runs before the tool and after kritrim's own checks, so it can
// narrow what an agent may do and never widen it. A policy cannot grant
// Shell a command its allowlist lacks, and it cannot waive an approval.
package authz

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/ganeshdipdumbare/kritrim"
)

// Request is one tool call awaiting a decision.
type Request struct {
	// User and Roles are the run's identity, from kritrim.User and
	// kritrim.Roles (or Config.User and Config.Roles).
	User  string
	Roles []string
	// Tool is the tool name, and Args are the arguments the model chose,
	// after any approval edited them.
	Tool string
	Args map[string]any
	// Thread and Run identify the conversation and the run.
	Thread string
	Run    string
}

// Text is a string argument, or "" when it is absent or another type.
func (r Request) Text(key string) string {
	s, _ := r.Args[key].(string)
	return s
}

// Number is a numeric argument, or 0 when it is absent or another type.
// JSON numbers arrive as float64, so this is the way to read one.
func (r Request) Number(key string) float64 {
	switch v := r.Args[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

// HasRole reports whether the caller has a role.
func (r Request) HasRole(role string) bool {
	for _, got := range r.Roles {
		if got == role {
			return true
		}
	}
	return false
}

// Policy is one rule. Build it with Permit or Forbid and narrow it with
// For, By, When and Because; the zero value is not a policy.
type Policy struct {
	forbid     bool
	actions    []string
	roles      []string
	principals []string
	when       func(Request) bool
	reason     string
}

// Permit allows the named tools. A name is matched with path.Match, so
// "*" is every tool and "file_*" is a family of them.
func Permit(actions ...string) Policy { return Policy{actions: actions} }

// Forbid denies the named tools, whatever else permits them.
func Forbid(actions ...string) Policy { return Policy{forbid: true, actions: actions} }

// For limits the policy to callers with one of these roles. Without it the
// policy applies to every role.
func (p Policy) For(roles ...string) Policy {
	p.roles = append(append([]string(nil), p.roles...), roles...)
	return p
}

// By limits the policy to these user ids, matched with path.Match.
func (p Policy) By(users ...string) Policy {
	p.principals = append(append([]string(nil), p.principals...), users...)
	return p
}

// When limits the policy to calls the condition accepts, which is where
// argument values belong: an amount ceiling, a path prefix, an allowed
// account. A condition that panics denies the call rather than taking the
// run down with it.
func (p Policy) When(fn func(Request) bool) Policy { p.when = fn; return p }

// Because sets the text the model is told when this policy denies a call.
// Write it as an explanation the model can pass on.
func (p Policy) Because(reason string) Policy { p.reason = reason; return p }

func (p Policy) validate() error {
	if len(p.actions) == 0 {
		return errors.New("authz: a policy needs at least one action")
	}
	for _, a := range p.actions {
		if strings.TrimSpace(a) == "" {
			return errors.New("authz: a policy action cannot be empty")
		}
		if _, err := path.Match(a, "x"); err != nil {
			return fmt.Errorf("authz: action pattern %q: %w", a, err)
		}
	}
	for _, u := range p.principals {
		if _, err := path.Match(u, "x"); err != nil {
			return fmt.Errorf("authz: user pattern %q: %w", u, err)
		}
	}
	return nil
}

// matches reports whether the policy covers this request.
func (p Policy) matches(r Request) bool {
	if !anyMatch(p.actions, r.Tool) {
		return false
	}
	if len(p.principals) > 0 && !anyMatch(p.principals, r.User) {
		return false
	}
	if len(p.roles) > 0 {
		hit := false
		for _, want := range p.roles {
			if r.HasRole(want) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return p.condition(r)
}

// condition runs When, treating a panic as "does not match" so a bad
// condition denies instead of ending the run.
func (p Policy) condition(r Request) (ok bool) {
	if p.when == nil {
		return true
	}
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return p.when(r)
}

func anyMatch(patterns []string, v string) bool {
	for _, pat := range patterns {
		if pat == v {
			return true
		}
		if ok, err := path.Match(pat, v); err == nil && ok {
			return true
		}
	}
	return false
}

// Set is a validated list of policies.
type Set struct{ policies []Policy }

// New validates the policies and returns the set. An invalid policy is an
// error here rather than a surprise at run time.
func New(policies ...Policy) (*Set, error) {
	if len(policies) == 0 {
		return nil, errors.New("authz: no policies; an empty set denies everything")
	}
	for i, p := range policies {
		if err := p.validate(); err != nil {
			return nil, fmt.Errorf("policy %d: %w", i+1, err)
		}
	}
	return &Set{policies: append([]Policy(nil), policies...)}, nil
}

// ErrDenied is the error Check returns for a call policy does not allow.
var ErrDenied = errors.New("denied by policy")

// Check decides one request. It returns nil when the call is allowed, and
// an error wrapping ErrDenied when it is not.
func (s *Set) Check(r Request) error {
	for _, p := range s.policies {
		if p.forbid && p.matches(r) {
			return fmt.Errorf("%w: %s", ErrDenied, p.why(r, "forbidden"))
		}
	}
	for _, p := range s.policies {
		if !p.forbid && p.matches(r) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s is not permitted%s%s", ErrDenied, r.Tool, forCaller(r), s.nearMiss(r))
}

// nearMiss explains a default deny with the Because text of the policies
// that cover this tool but not this call. A ceiling the caller went over
// has an author who wrote down why; the model needs those words to pass on,
// and "not permitted" on its own tells the user nothing they can act on.
func (s *Set) nearMiss(r Request) string {
	var seen []string
	for _, p := range s.policies {
		if p.forbid || p.reason == "" || !anyMatch(p.actions, r.Tool) {
			continue
		}
		if !contains(seen, p.reason) {
			seen = append(seen, p.reason)
		}
	}
	if len(seen) == 0 {
		return ""
	}
	return ". The rules for it: " + strings.Join(seen, "; ")
}

func contains(list []string, v string) bool {
	for _, got := range list {
		if got == v {
			return true
		}
	}
	return false
}

func (p Policy) why(r Request, fallback string) string {
	if p.reason != "" {
		return p.reason
	}
	return r.Tool + " is " + fallback + forCaller(r)
}

func forCaller(r Request) string {
	switch {
	case r.User != "" && len(r.Roles) > 0:
		return fmt.Sprintf(" for %s (%s)", r.User, strings.Join(r.Roles, ", "))
	case r.User != "":
		return " for " + r.User
	case len(r.Roles) > 0:
		return " for " + strings.Join(r.Roles, ", ")
	}
	return " for an unidentified caller"
}

// Hooks returns the hook set that enforces the policies. Compose it with
// others through kritrim.Chain, and put it first so observers only ever
// see calls policy allowed:
//
//	Hooks: kritrim.Chain(policy.Hooks(), trace.OTel(cfg))
func (s *Set) Hooks() kritrim.Hooks {
	return kritrim.Hooks{
		BeforeTool: func(ctx context.Context, e *kritrim.ToolEvent) error {
			args := e.Args
			if args == nil && e.Call != nil {
				args = e.Call.Args
			}
			name := ""
			if e.Call != nil {
				name = e.Call.Name
			}
			r := Request{
				User:   kritrim.UserFrom(ctx),
				Roles:  kritrim.RolesFrom(ctx),
				Tool:   name,
				Args:   args,
				Thread: kritrim.ThreadFrom(ctx),
				Run:    kritrim.RunIDFrom(ctx),
			}
			if err := s.Check(r); err != nil {
				// Deny, not an error: the model is told and carries on.
				e.Deny = strings.TrimPrefix(err.Error(), ErrDenied.Error()+": ")
			}
			return nil
		},
	}
}
