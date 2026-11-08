// Package kritrim is a small, secure-by-default agent SDK for Go.
//
// An agent is a model, instructions, tools and a session store. Every
// built-in tool reaches the network, files or processes through one broker,
// secrets are resolved only where policy allows, and every model and tool
// call can be written to a tamper-evident audit log.
//
//	a, _ := kritrim.New(kritrim.Config{
//		Model:        kritrim.Anthropic("claude-sonnet-5-5"),
//		Instructions: "You are concise.",
//	})
//	defer a.Close()
//	res, _ := a.Run(ctx, "Hello")
//	fmt.Println(res.Text)
package kritrim

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Config is everything an agent is made of.
type Config struct {
	// Model is required. Use OpenAI, Anthropic, Gemini, Ollama, Compatible or Mock.
	Model        Model
	Instructions string

	// Tools are the tools the model may call.
	Tools []Tool

	// Session stores conversations. Default: memory, lost on exit.
	Session SessionStore

	// Approve is asked before a risky tool runs. When nil, a risky tool is
	// refused with an error result instead of running.
	Approve func(ctx context.Context, req Approval) (Decision, error)

	// Audit receives one hash-chained entry per model, tool and approval.
	Audit AuditSink

	// Sandbox runs Shell and Files inside the sandbox instead of the host.
	Sandbox Sandbox

	// Sealed runs the whole agent inside the sandbox. The host only brokers
	// model calls, secrets, host tools, audit and approvals.
	Sealed *Sealed

	Limits Limits

	// Secrets are explicit secret values; a name missing here falls back
	// to the environment.
	Secrets map[string]string

	// Client is the HTTP client used by the built-in tools.
	Client *http.Client

	// MaxOutputTokens caps one model reply.
	MaxOutputTokens int
	// Temperature and ToolChoice are passed to the model.
	Temperature *float64
	ToolChoice  string

	// User and Roles are the default identity for runs.
	User  string
	Roles []string

	// Logger receives one line per notable event; nil discards.
	Logger func(msg string)
}

// Limits caps one run. Zero means the default (Steps 20, ToolCalls 100) or
// no limit (Tokens).
type Limits struct {
	Steps     int
	ToolCalls int
	Tokens    int
}

// Sealed runs the whole agent inside a sandbox. No API key, secret value or
// host file ever enters the box.
type Sealed struct {
	Sandbox Sandbox
	// Binary is the program the box runs; it must be built for the box OS
	// and architecture. Default: the running executable.
	Binary string
	// Egress lists hosts the box may reach through the host proxy without a
	// secret, e.g. "api.example.com" or "*.example.com".
	Egress []string
	// Secrets maps a secret name to the hosts its value may be sent to.
	Secrets map[string][]string
	// MaxTokens caps total tokens for the whole sealed agent.
	MaxTokens int
	// Bridged lists host tools the box may call. nil means none.
	Bridged []string
	// AllowUnsafeLocal permits a local, unsandboxed box. Development only.
	AllowUnsafeLocal bool
}

// Agent is a started composition.
type Agent struct {
	cfg      Config
	model    Model
	session  SessionStore
	env      env
	machine  Machine
	redactor *redactor
	box      *boxHost
	close    sync.Once
}

// New validates the configuration and starts the agent. Nothing runs until
// a call to Run or Stream.
func New(cfg Config) (*Agent, error) {
	if InBox() {
		return nil, errors.New("kritrim: this process is a sealed box; call kritrim.ServeBox instead of kritrim.New")
	}
	if cfg.Sealed != nil {
		return newSealed(cfg)
	}
	if cfg.Model == nil {
		return nil, errors.New("kritrim: no model; set Config.Model")
	}
	if cfg.Limits.Steps == 0 {
		cfg.Limits.Steps = 20
	}
	if cfg.Limits.ToolCalls == 0 {
		cfg.Limits.ToolCalls = 100
	}
	if cfg.Session == nil {
		cfg.Session = MemorySessions()
	}
	seen := map[string]bool{}
	for _, t := range cfg.Tools {
		if err := t.validate(); err != nil {
			return nil, err
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("kritrim: duplicate tool %q", t.Name)
		}
		seen[t.Name] = true
	}
	a := &Agent{cfg: cfg, model: cfg.Model, session: cfg.Session, redactor: &redactor{}}
	if cfg.Sandbox != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		m, err := cfg.Sandbox.Open(ctx, OpenConfig{})
		if err != nil {
			return nil, fmt.Errorf("kritrim: open sandbox: %w", err)
		}
		a.machine = m
	}
	a.env = newHostEnv(a)
	return a, nil
}

// Close releases the sandbox and the sealed box. Safe to call twice.
func (a *Agent) Close() error {
	var err error
	a.close.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if a.box != nil {
			err = a.box.close()
			return
		}
		if a.machine != nil {
			err = a.machine.Close(ctx)
		}
	})
	return err
}

// Model reports the model the agent was built with.
func (a *Agent) Model() string {
	if m, ok := a.model.(interface{ Name() string }); ok {
		return m.Name()
	}
	return ""
}

func logf(cfg Config, format string, args ...any) {
	if cfg.Logger != nil {
		cfg.Logger(fmt.Sprintf(format, args...))
	}
}

// envOrDefaults fills empty per-run identity from Config.
func (a *Agent) identity(o *runOptions) {
	if o.user == "" {
		o.user = a.cfg.User
	}
	if len(o.roles) == 0 {
		o.roles = a.cfg.Roles
	}
}
