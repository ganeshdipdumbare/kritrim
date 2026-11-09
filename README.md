# kritrim

**A small, secure-by-default agent SDK for Go.** One model, one tool
constructor, one run call. Every built-in capability goes through one broker,
and the whole agent can run inside a sandbox that reaches the outside world
only through the host.

```go
a, err := kritrim.New(kritrim.Config{
    Model:        kritrim.Anthropic("claude-sonnet-5-5"),
    Instructions: "You are concise.",
})
defer a.Close()

res, err := a.Run(ctx, "Hello")
fmt.Println(res.Text)
```

Requires **Go 1.27**. No third-party dependencies.

## Install

```bash
go get github.com/ganeshdipdumbare/kritrim
```

Private repository: run `go env -w GOPRIVATE=github.com/ganeshdipdumbare/*`
once, and `gh auth setup-git` (or use an SSH key).

## The core API

```go
kritrim.New(Config) (*Agent, error)
(*Agent).Run(ctx, prompt, ...Option) (*Result, error)
(*Agent).Stream(ctx, prompt, ...Option) iter.Seq2[Event, error]
(*Agent).AsTool(name, description) Tool
(*Agent).WithTools(...Tool) *Agent
(*Agent).Close() error

kritrim.NewTool(name, description, fn) Tool
kritrim.Risky(Tool) Tool
kritrim.SchemaOf(v) map[string]any

kritrim.Thread(id) Option
kritrim.Output(&v) Option
kritrim.Decisions(map[string]Decision) Option
kritrim.OnEvent(func(Event)) Option
kritrim.User(id) Option
kritrim.Roles(...) Option
kritrim.Images(...) Option

kritrim.Approve() / Reject(why) / Edit(args) / Respond(text) / Pause()

kritrim.MemorySessions() SessionStore
kritrim.FileSessions(dir) SessionStore
kritrim.SQLSessions(db, SQLConfig) (SessionStore, error)
kritrim.AuditFile(path) AuditSink
kritrim.VerifyAudit(path) error

kritrim.Time() Tool
kritrim.HTTP(HTTPConfig) Tool
kritrim.WebSearch(WebSearchConfig) Tool
kritrim.Shell(ShellConfig) Tool
kritrim.Files(FilesConfig) Tool
kritrim.Read(ReadConfig) Tool
kritrim.Write(WriteConfig) Tool
kritrim.EditFile(EditConfig) Tool
kritrim.Sleep(max) Tool
kritrim.Stop() Tool

kritrim.Local() Sandbox
kritrim.Docker(DockerConfig) Sandbox
kritrim.InBox() bool
kritrim.ServeBox(ctx, stdin, stdout, cfg) error
```

There is no module registry, no provider catalog, and no second way to do any
of the above.

## Models

```go
kritrim.OpenAI("gpt-4.1")                  // OPENAI_API_KEY
kritrim.Anthropic("claude-sonnet-5-5")     // ANTHROPIC_API_KEY
kritrim.Gemini("gemini-2.5-pro")           // GEMINI_API_KEY
kritrim.Ollama("llama3")                   // localhost:11434
kritrim.DeepSeek("deepseek-chat")
kritrim.Groq("llama-3.3-70b-versatile")
kritrim.Mistral("mistral-large-latest")
kritrim.OpenRouter("anthropic/claude-sonnet-5-5")
kritrim.LiteLLM("http://gateway:4000", "gpt-4.1")
kritrim.Compatible("provider", "https://host/v1", "model")
kritrim.Mock(turns...)                     // offline
```

Each constructor takes an optional `ModelConfig{APIKey, BaseURL, Headers,
Client, Timeout, JSONMode}`. Streaming, tool calls, images, usage and native
structured output are implemented for OpenAI-compatible, Anthropic and Gemini.
`Config.Fallback` tries the next model when a call keeps failing with a
retryable error.

## Tools

```go
func weather(ctx context.Context, in struct {
    City string `json:"city" desc:"city name"`
}) (string, error) {
    return "18C and light rain in " + in.City, nil
}

a, _ := kritrim.New(kritrim.Config{
    Model: kritrim.Mock(kritrim.TurnTool("weather", map[string]any{"city": "Lisbon"}), kritrim.Turn{Text: "Raining."}),
    Tools: []kritrim.Tool{kritrim.NewTool("weather", "Get the weather", weather)},
})
```

The JSON Schema comes from the Go type (`json`, `desc`, `enum`). Mark a tool
`Risky` to require approval. Built-in tools include time, HTTP, web search,
shell, files, line-numbered read, write, edit, sleep and stop.

## Streaming

```go
for e, err := range a.Stream(ctx, "Plan a trip") {
    if err != nil { break }
    switch e.Type {
    case "text":        fmt.Print(e.Text)
    case "tool_call":   fmt.Println("->", e.ToolCall.Name)
    case "tool_result": fmt.Println("<-", e.ToolResult.Content)
    case "approval":    fmt.Println("?", e.Approval.Message)
    }
}
```

Event types: `text`, `reasoning`, `tool_call`, `tool_result`, `approval`,
`step_start`, `step_end`, `finish`, `interrupt`, `error`. Breaking out early
cancels the run.

## Sessions

```go
a, _ := kritrim.New(kritrim.Config{
    Model:   "…",
    Session: kritrim.FileSessions(".agent/sessions"), // or MemorySessions / SQLSessions
})
a.Run(ctx, "My name is Ada.", kritrim.Thread("user-42"))
a.Run(ctx, "What is my name?", kritrim.Thread("user-42"))
```

Sessions are version-checked: two writers racing get `ErrSessionConflict` and
the second can retry. `SQLSessions` uses `database/sql`; import your driver.

## Durable approvals

Synchronous approvals use `Config.Approve`. To pause and resume later:

```go
a, _ := kritrim.New(kritrim.Config{
    Model:   "…",
    Tools:   []kritrim.Tool{kritrim.Risky(refund)},
    Session: kritrim.FileSessions(".agent"),
    Approve: func(ctx context.Context, req kritrim.Approval) (kritrim.Decision, error) {
        return kritrim.Pause(), nil // persist and return interrupts
    },
})
res, _ := a.Run(ctx, "Refund A-17", kritrim.Thread("ticket-981"))
// res.Stop == "interrupt"; res.Interrupts[0].ID

// Later, in another process:
res, _ = a.Run(ctx, "", kritrim.Thread("ticket-981"),
    kritrim.Decisions(map[string]kritrim.Decision{
        res.Interrupts[0].ID: kritrim.Approve(),
    }))
```

Resume runs only the remaining tools; the model is not called again. Decision
IDs are stable, a second resume gets a session conflict, and a paused thread
requires `Decisions` (a new prompt is refused).

## Structured output

```go
type Trip struct {
    City    string   `json:"city"`
    Packing []string `json:"packing" desc:"things to pack"`
}

var trip Trip
_, err := a.Run(ctx, "Plan Lisbon", kritrim.Output(&trip))
```

Native JSON schema where supported, one repair call when parsing fails, then
`ErrOutput`.

## Retry, context and hooks

```go
a, _ := kritrim.New(kritrim.Config{
    Model:   "…",
    Retry:   &kritrim.Retry{Max: 4}, // default: 3 attempts, jittered backoff
    Context: &kritrim.ContextConfig{MaxTokens: 60_000, KeepLast: 30},
    Hooks: kritrim.Hooks{
        BeforeModel: func(ctx context.Context, e *kritrim.ModelEvent) error { return nil },
        AfterTool:   func(ctx context.Context, e *kritrim.ToolEvent) { /* audit/metrics */ },
    },
})
```

Hooks run after the built-in security checks; they can mutate requests,
responses and tool results but cannot grant a capability policy denied.
`Context` trims old messages and optionally summarizes dropped history.

Tools and hooks can read per-session state:

```go
kritrim.StateFrom(ctx).Set("seen", true)
```

## Security

- **Default deny.** `HTTP` and `Shell` refuse to be constructed without an
  allowlist. `Files` is read-only unless `Write: true`.
- **SSRF and redirects.** Exact allowlisted hosts may be private; wildcards
  resolve and must be public; every redirect is re-validated.
- **No shell interpretation.** `Shell` takes `argv`; `argv[0]` must be allowed.
- **Secrets stay out of prompts.** `secret://NAME` is resolved at use and
  redacted from output, events and audit.
- **Approvals.** Risky tools need `Config.Approve`; without it they are refused.
- **Limits.** Steps, tool calls, tokens, per-tool timeout, panic recovery.
- **Tamper-evident audit.** Hash-chained JSON lines; `VerifyAudit` checks them.
- **PII hooks.** `guard.PII("email", "credit_card")` redacts prompts, replies
  and tool results.
- **Tracing.** `trace.JSON(w)` writes one line per run/model/tool event;
  `trace.OTLP(...)` exports one OTLP/HTTP trace per run to a collector.
- **User Go tools are your code.** The broker governs built-in tools; put
  untrusted work in a sandbox.

## Sandboxes

### Tools in a sandbox

```go
a, _ := kritrim.New(kritrim.Config{
    Model:   "…",
    Tools:   []kritrim.Tool{kritrim.Shell(kritrim.ShellConfig{Allow: []string{"go", "git"}})},
    Sandbox: kritrim.Docker(kritrim.DockerConfig{Image: "golang:1.27"}),
})
```

`Local` exists for development. `sandbox/ssh` runs commands and files on a
remote host with host-key checking and an SSH option allowlist. Every adapter
must pass `sandbox/sandboxtest.Run`.

### The agent in a sandbox (sealed)

```
host process                                 sandbox
┌──────────────────────────────┐             ┌─────────────────────────────────┐
│ API keys, secrets, policy    │  JSON-RPC   │ agent loop, session messages,   │
│ model providers, token budget│◄───────────►│ local shell and files           │
│ audit, approvals, egress     │   stdio     │ no keys, no host FS, no network │
└──────────────────────────────┘             └─────────────────────────────────┘
```

```go
cfg := kritrim.Config{
    Model: "anthropic:claude-sonnet-5-5",
    Tools: []kritrim.Tool{kritrim.Shell(kritrim.ShellConfig{Allow: []string{"ls"}})},
    Sealed: &kritrim.Sealed{
        Sandbox:   kritrim.Docker(kritrim.DockerConfig{Image: "debian:bookworm-slim"}),
        Binary:    "./box", // GOOS=linux build; kritrim box build -o box .
        Egress:    []string{"api.github.com"},
        Secrets:   map[string][]string{"GITHUB_TOKEN": {"api.github.com"}},
        MaxTokens: 200_000,
        Bridged:   []string{"crm_lookup"},
    },
}
if kritrim.InBox() {
    log.Fatal(kritrim.ServeBox(ctx, os.Stdin, os.Stdout, cfg))
}
a, err := kritrim.New(cfg)
```

The box has no API key, no secret value, no host filesystem and no network by
default. Model calls, token budgets, secrets, egress, approvals, audit and
bridged tools all cross the host. Sessions stay on the host, so a box is
disposable and durable approvals survive a box restart.

For containers, build the box with `go run ./cmd/kritrim box build -o box .`
(default `linux/amd64`). `Local` needs no binary and requires
`AllowUnsafeLocal: true`.

### Third-party providers

The contract is `Sandbox` + `Machine` + `Process`; see
[`sandbox/README.md`](sandbox/README.md). E2B, microsandbox, Firecracker,
Tensorlake and Daytona plug in without core changes and must pass the
conformance suite.

## MCP

```go
a, _ := kritrim.New(kritrim.Config{
    Model: "…",
    MCP: []kritrim.MCPConfig{
        {Name: "github", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}},
        {Name: "docs", URL: "https://mcp.example.com/mcp",
            Headers: map[string]string{"Authorization": "Bearer secret://MCP_TOKEN"}},
    },
})
```

stdio and Streamable HTTP are supported, with tool namespacing
(`server__tool`), per-call timeouts, host allowlists and `Risky`. In sealed
mode the host connects and bridges the tools into the box automatically.

## Multi-agent

```go
// Agents as tools
main.Tools = append(main.Tools, expert.AsTool("expert", "Answers Lisbon questions"))

// Graph
g := multiagent.NewGraph().Start("triage").
    Node("triage", triageNode).
    Node("billing", billingNode).
    Edge("triage", "billing", func(s *multiagent.State) bool { return s.Get("intent") == "billing" })
res, _ := g.Run(ctx, "invoice question")

// Swarm
s, _ := multiagent.NewSwarm(
    multiagent.Member{Name: "researcher", Description: "finds facts", Agent: researcher},
    multiagent.Member{Name: "writer", Description: "writes", Agent: writer},
)
res, _ = s.Run(ctx, "draft the launch post")
```

Swarm members get `handoff_to_<name>` tools. Workflows are plain Go; there is
no extra primitive.

## A2A

```go
// Server
srv, _ := a2a.NewServer(a2a.ServerConfig{
    Card:  a2a.Card{Name: "Triage"},
    Auth:  a2a.Bearer(os.Getenv("A2A_TOKEN")),
    Agent: func(contextID string) (*kritrim.Agent, error) { return buildAgent(), nil },
})
http.ListenAndServe(":9000", srv)

// Client
c, _ := a2a.NewClient(a2a.ClientConfig{Endpoint: "https://triage.internal",
    Headers: map[string]string{"Authorization": "Bearer secret://A2A_TOKEN"}})
res, _ := c.Run(ctx, "ticket 42")
tool := c.AsTool("triage", "Routes a ticket")
```

Agent card, `message/send`, `message/stream`, `tasks/get`, `tasks/cancel`, text
and data parts, per-context isolation, and interrupt/resume are implemented.
Auth is required; contexts are not an auth boundary.

## Evals

```go
report, _ := evals.Experiment{
    Cases: []evals.Case{{Name: "capital", Input: "Capital of France?", Expected: "Paris"}},
    Evaluators: []evals.Evaluator{
        evals.Contains(),
        evals.ToolCalls(evals.ToolCallConfig{Require: []string{"lookup"}}),
        evals.LLMJudge(evals.JudgeConfig{Model: judge, Rubric: "1.0 if correct"}),
    },
    Concurrency: 4,
    Detect:      true,
    Cache:       evals.DirCache(".agent/evals"),
}.Run(ctx, func(ctx context.Context, c evals.Case) (*kritrim.Result, error) {
    return agent.Run(ctx, c.Input, kritrim.Thread(c.Name))
})
fmt.Print(report.Summary())
```

Deterministic evaluators, tool-trajectory checks, token/latency budgets, an
LLM judge, detectors, a JSON/Markdown report and a disk cache. `evals.RunRedTeam`
runs prompt-injection, exfiltration and destructive-tool probes. The CLI
renders and diagnoses reports:

```bash
go run ./cmd/kritrim-evals report report.json
go run ./cmd/kritrim-evals diagnose report.json
```

## Triggers

```go
sched, _ := trigger.NewScheduler(agent,
    trigger.Schedule{Every: time.Hour, Thread: "daily", Prompt: "summarize"})
sched.Start(ctx)
defer sched.Close()

hook, _ := trigger.Webhook(trigger.WebhookConfig{Agent: agent, Secret: "secret://HOOK_SECRET"})
http.Handle("/hook", hook)
```

Webhooks require an HMAC signature and deduplicate `Idempotency-Key` retries.

## Memory

```go
store, _ := memory.NewFile(".agent/memory")
a, _ := kritrim.New(kritrim.Config{Model: "…", Tools: memory.Tools(store)})
```

`remember` and `recall` tools backed by a lexical memory store (memory or
file). No vector database.

## Examples

```bash
go run ./examples/quickstart
go run ./examples/tools
go run ./examples/full
go run ./examples/sealed
go run ./examples/a2a
go run ./examples/evals
go run ./examples/multiagent
```

## Test

```bash
go test ./...
go test -race ./...
go vet ./...
```

## Status

Released: core loop, providers, tools, sessions (memory/file/SQL), durable
approvals, hooks, retry, context, state, audit, PII, tracing, Docker/SSH/Local
sandboxes, sealed mode, MCP, A2A, evals, graph/swarm, triggers, memory.

Not shipped yet: E2B/microsandbox/Firecracker/Tensorlake sandbox adapters (the
conformance kit and interface are ready), A2A push notifications and file
parts, evals simulators/red-team and case generation, OTel exporters (the
trace hooks are the seam), Bedrock and OpenAI Responses providers, prompt
caching, voice. See [`docs/ROADMAP.md`](docs/ROADMAP.md).
