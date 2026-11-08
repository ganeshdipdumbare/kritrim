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

The repository is private: run `go env -w GOPRIVATE=github.com/ganeshdipdumbare/*`
once, and `gh auth setup-git` (or use an SSH key).

## The whole API

```go
kritrim.New(Config) (*Agent, error)
(*Agent).Run(ctx, prompt, ...Option) (*Result, error)
(*Agent).Stream(ctx, prompt, ...Option) iter.Seq2[Event, error]
(*Agent).Close() error

kritrim.NewTool(name, description, fn) Tool
kritrim.Risky(Tool) Tool

kritrim.Thread(id) Option    // stored conversation
kritrim.Output(&v) Option    // structured output
kritrim.User(id) Option
kritrim.Roles(...) Option
kritrim.Images(...) Option

kritrim.Approve() Decision
kritrim.Reject(reason) Decision

kritrim.MemorySessions() SessionStore   // default
kritrim.FileSessions(dir) SessionStore
kritrim.AuditFile(path) AuditSink
kritrim.VerifyAudit(path) error

kritrim.Time() Tool
kritrim.HTTP(HTTPConfig) Tool
kritrim.WebSearch(WebSearchConfig) Tool
kritrim.Shell(ShellConfig) Tool
kritrim.Files(FilesConfig) Tool

kritrim.Local() Sandbox
kritrim.Docker(DockerConfig) Sandbox

kritrim.InBox() bool
kritrim.ServeBox(ctx, stdin, stdout, cfg) error
```

There is no module registry, no provider catalog, no `Run` struct, and no
second way to do any of the above.

## Models

```go
kritrim.OpenAI("gpt-4.1")                  // OPENAI_API_KEY
kritrim.Anthropic("claude-sonnet-5-5")     // ANTHROPIC_API_KEY
kritrim.Gemini("gemini-2.5-pro")           // GEMINI_API_KEY
kritrim.Ollama("llama3")                   // localhost:11434
kritrim.Compatible("deepseek", "https://api.deepseek.com/v1", "deepseek-chat")
kritrim.Mock(turns...)                     // offline, for tests and examples
```

Each constructor takes an optional `ModelConfig{APIKey, BaseURL, Headers,
Client, Timeout, JSONMode}`. Streaming, tool calls, images, usage and native
structured output are implemented for all three providers.

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

The JSON Schema comes from the Go type (`json`, `desc` and `enum` tags). A
string result goes to the model as is; anything else is JSON.

Mark a tool `kritrim.Risky(t)` to make `Config.Approve` run before it. When no
approver is configured, a risky tool is refused instead of running.

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
`step_start`, `step_end`, `finish`, `error`. Breaking out early cancels the
run.

## Sessions

```go
a, _ := kritrim.New(kritrim.Config{
    Model:   "…",
    Session: kritrim.FileSessions(".agent/sessions"),
})
a.Run(ctx, "My name is Ada.", kritrim.Thread("user-42"))
a.Run(ctx, "What is my name?", kritrim.Thread("user-42")) // sees the first turn
```

Omit `Thread` for a one-off run. The default store is memory. Sessions are
plain JSON, so a different process can continue them.

## Structured output

```go
type Trip struct {
    City    string   `json:"city"`
    Packing []string `json:"packing" desc:"things to pack"`
}

var trip Trip
_, err := a.Run(ctx, "Plan Lisbon", kritrim.Output(&trip))
```

The schema is sent natively where the provider supports it (OpenAI JSON
schema, Gemini response schema, Anthropic forced tool). One repair call is
made when the reply does not parse; after that the run returns `ErrOutput`.

## Security

Enabling a built-in capability *is* the grant. There is no second policy file
to drift from the code.

- **Default deny.** `HTTP` and `Shell` refuse to be constructed without an
  allowlist. `Files` is read-only unless `Write: true`.
- **SSRF guard.** An exact allowlisted host may be private; wildcard matches
  resolve and must be public.
- **No shell interpretation.** `Shell` takes `argv`; `argv[0]` must be in the
  allowlist. `sh -c` is only possible if `sh` is allowed.
- **Secrets stay out of prompts.** `secret://NAME` is resolved at the point of
  use and redacted from tool output, events and audit details.
- **Approvals.** `Config.Approve` decides before a risky tool runs.
- **Limits.** Steps, tool calls and tokens are capped per run.
- **Tamper-evident audit.** One hash-chained JSON line per model call, tool
  call, approval and run, naming user and thread. `VerifyAudit` checks the
  chain.
- **User Go tools are your code.** The broker governs the SDK's built-in
  tools; it cannot sandbox arbitrary code you compile into the binary. Put
  untrusted work in a sandbox.

## Sandboxes

### Tools in a sandbox

```go
a, _ := kritrim.New(kritrim.Config{
    Model:   "…",
    Tools:   []kritrim.Tool{kritrim.Shell(kritrim.ShellConfig{Allow: []string{"go", "git"}}), kritrim.Files(kritrim.FilesConfig{Root: "/workspace"})},
    Sandbox: kritrim.Docker(kritrim.DockerConfig{Image: "golang:1.27"}),
})
```

Shell and file operations run in the container; model calls, secrets and audit
stay on the host. The container is created with no network and removed on
`Close`.

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
    Model: kritrim.Anthropic("claude-sonnet-5-5"),
    Tools: []kritrim.Tool{kritrim.Shell(kritrim.ShellConfig{Allow: []string{"ls"}}), kritrim.Time()},
    Sealed: &kritrim.Sealed{
        Sandbox:   kritrim.Docker(kritrim.DockerConfig{Image: "debian:bookworm-slim"}),
        Egress:    []string{"api.github.com"},
        Secrets:   map[string][]string{"GITHUB_TOKEN": {"api.github.com"}},
        MaxTokens: 200_000,
        Bridged:   []string{"crm_lookup"}, // host tools the box may call
    },
}

if kritrim.InBox() { // the box runs this same program
    log.Fatal(kritrim.ServeBox(ctx, os.Stdin, os.Stdout, cfg))
}
a, err := kritrim.New(cfg)
```

In sealed mode the box has:

- no API key: every model call is proxied and counted by the host;
- no secret value: it gets a placeholder the host swaps only for allowed hosts;
- no host filesystem and no network by default;
- no host tools unless named in `Bridged`;
- the audit log and approval decisions written by the host.

Sessions stay on the host: the host sends history in and stores the result, so
a box is disposable. `Output` is decoded on the host. The box binary must be
built for the box (`GOOS=linux` for containers); `Sealed.Binary` selects it,
and the default is the running executable (correct for `Local`).

**Local** (`kritrim.Local()`) is not an isolation boundary. It exists for
development and requires `Sealed.AllowUnsafeLocal: true`.

### Third-party sandbox providers

The contract is five methods; anything that can run a process, copy files and
be destroyed fits — E2B, microsandbox, Firecracker, Tensorlake, Daytona, or a
cloud VM.

```go
type Sandbox interface {
    Open(ctx context.Context, opts OpenConfig) (Machine, error)
}
type Machine interface {
    Exec(ctx context.Context, req ExecRequest) (ExecResult, error)
    Start(ctx context.Context, req StartRequest) (Process, error) // stdio
    Read(ctx context.Context, path string) ([]byte, error)
    Write(ctx context.Context, path string, data []byte, mode fs.FileMode) error
    Close(ctx context.Context) error // must destroy the machine
}
```

See [`sandbox/README.md`](sandbox/README.md) for the invariants a provider
must hold.

## Examples

```bash
go run ./examples/quickstart   # no keys, no network
go run ./examples/tools        # typed tool + streaming
go run ./examples/full         # sessions, structured output, approval, audit
go run ./examples/sealed       # the whole agent in a sandbox
```

## Status

v0.1.0. Deliberately not here yet: MCP, multi-agent graphs/swarms, triggers and
webhooks, SQL session stores, PII/guardrail packs, and cloud sandbox adapters.
The core does not block any of them.

## Test

```bash
go test ./...          # offline; Docker tests skip when Docker is absent
go vet ./...
```
