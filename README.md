<p align="center"><img src="docs/assets/img/mark.svg" width="72" alt="kritrim"></p>

<h1 align="center">kritrim</h1>

<p align="center"><b>Docs:</b> <a href="https://kritrim-docs.vercel.app">kritrim-docs.vercel.app</a></p>

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

Requires **Go 1.27** (build servers with 1.27.4 or later for the
`net/http` fix GO-2026-6859). No third-party dependencies.

## Install

```bash
go get github.com/ganeshdipdumbare/kritrim
```

kritrim follows semantic versioning: v1 keeps its API stable (see
[`docs/API.md`](docs/API.md)), and [`docs/MIGRATING.md`](docs/MIGRATING.md)
covers the move from v0.9. MIT licensed.

## The core API

```go
kritrim.New(Config) (*Agent, error)
(*Agent).Run(ctx, prompt, ...Option) (*Result, error)
(*Agent).Stream(ctx, prompt, ...Option) iter.Seq2[Event, error]
(*Agent).AsTool(name, description) Tool
(*Agent).WithTools(...Tool) *Agent
(*Agent).CallTool(ctx, name, args, ...Option) (string, error)
(*Agent).Fork(ctx, from, to) error
(*Agent).Session(ctx, id) (*Session, error)
(*Agent).Cancel(runID) bool
(*Agent).Close() error

kritrim.NewTool(name, description, fn) Tool
kritrim.NewRawTool(name, description, schema, fn) Tool
kritrim.Risky(Tool) Tool
kritrim.SchemaOf(v) map[string]any

kritrim.Thread(id) Option
kritrim.Output(&v) Option
kritrim.Decisions(map[string]Decision) Option
kritrim.OnEvent(func(Event)) Option
kritrim.RunID(id) Option
kritrim.Idempotency(key) Option
kritrim.User(id) Option
kritrim.Roles(...) Option
kritrim.Images(...) Option

kritrim.Approve() / Reject(why) / Edit(args) / Respond(text) / Pause()

kritrim.MemorySessions() SessionStore
kritrim.FileSessions(dir) SessionStore
kritrim.SQLSessions(db, SQLConfig) (SessionStore, error)
kritrim.SignedSessions(store, key) SessionStore
kritrim.AuditFile(path, ...AuditOption) AuditSink
kritrim.AuditKey(key) AuditOption
kritrim.VerifyAudit(path, ...AuditOption) error
kritrim.Chain(...Hooks) Hooks

kritrim.KeepWindow() Strategy
kritrim.Summarize(fn) Strategy
kritrim.SummarizeWith(model, instructions) Strategy
kritrim.EstimateTokens(msgs) int

kritrim.Time() Tool
kritrim.HTTP(HTTPConfig) Tool
kritrim.WebFetch(WebFetchConfig) Tool
kritrim.WebSearch(WebSearchConfig) Tool
kritrim.Shell(ShellConfig) Tool
kritrim.Files(FilesConfig) Tool
kritrim.Read(ReadConfig) Tool
kritrim.Write(WriteConfig) Tool
kritrim.EditFile(EditConfig) Tool
kritrim.Sleep(max) Tool
kritrim.Stop() Tool
kritrim.Notebook() Tool
kritrim.Todos() Tool
kritrim.Python(PythonConfig) Tool
kritrim.AskUser() Tool

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
kritrim.OpenAIResponses("gpt-5")           // Responses API
kritrim.Anthropic("claude-sonnet-5-5")     // ANTHROPIC_API_KEY
kritrim.Gemini("gemini-2.5-pro")           // GEMINI_API_KEY
kritrim.Ollama("llama3")                   // localhost:11434
kritrim.DeepSeek("deepseek-chat")
kritrim.Groq("llama-3.3-70b-versatile")
kritrim.Mistral("mistral-large-latest")
kritrim.OpenRouter("anthropic/claude-sonnet-5-5")
kritrim.LiteLLM("http://gateway:4000", "gpt-4.1")
kritrim.Bedrock("anthropic.claude-sonnet-5-5") // AWS SigV4, Converse API
kritrim.Compatible("provider", "https://host/v1", "model")
kritrim.Mock(turns...)                     // offline
```

Each constructor takes an optional `ModelConfig{APIKey, BaseURL, Headers,
Client, Timeout, JSONMode}`. Streaming, tool calls, images, usage and native
structured output are implemented for OpenAI-compatible, Anthropic and Gemini.

### Reasoning

```go
Reasoning: &kritrim.Reasoning{Effort: "high"} // low | medium | high
```

One setting, five providers: `Effort` becomes Anthropic extended thinking,
OpenAI reasoning effort, Gemini `thinkingConfig` and Bedrock
`reasoning_config`. `Budget` sets the thinking-token allowance exactly
(effort defaults: 1024 / 4096 / 16384); `Include: true` streams the thinking
back as `reasoning` events where the provider returns it.

Thinking tokens are output tokens. They count against `MaxOutputTokens` and
the run's budget, so a budget that cannot hold the thinking allowance is
refused before the call rather than capped into a provider error. Anthropic
needs its thinking blocks replayed with their signature on the next call, so
kritrim keeps them on the assistant message and sends them back — without
that, a reasoning model with tools is rejected mid-conversation.

### Guardrails

```go
kritrim.Bedrock("anthropic.claude-sonnet-5-5", kritrim.ModelConfig{
    Guardrail: &kritrim.Guardrail{ID: "gr-123", Version: "1"},
})
hooks, _ := guard.Bedrock(guard.BedrockConfig{ID: "gr-123", Version: "1", Tools: true}) // any model
```

`ModelConfig.Guardrail` runs a Bedrock guardrail inside the Converse call;
other providers refuse it at `New` rather than ignore it. `guard.Bedrock`
checks prompts, replies and tool results of any model with ApplyGuardrail
and fails closed. When a provider's safety system blocks a reply (Bedrock,
OpenAI `content_filter`, Anthropic `refusal`, Gemini `SAFETY`), the run ends
with `StopGuardrail` and that reply's tool calls are not run.

### Routing and fallback

```go
Model: small,
Route: func(ctx context.Context, prompt string) kritrim.Model {
    if len(prompt) > 500 { return large }
    return nil // nil means Config.Model
},
Fallback: []kritrim.Model{backup},
```

`Route` decides up front, per run, from the prompt and the context; it is
how a cheap model takes the easy questions. `Fallback` reacts after the
fact, trying the next model when a call keeps failing with a retryable
error. They compose: fallbacks apply to whatever `Route` chose, and the
audit log records the model the run actually used.

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

The JSON Schema comes from the Go type (`json`, `desc`, `enum`), and the
model's arguments are validated against it before the tool runs (types,
required, enum/const, lengths, pattern, ranges, combinators and local
`$ref`; a keyword that cannot be enforced fails the call). Mark a tool `Risky` to require approval. `ToolConcurrency > 1` runs the
model's parallel tool calls concurrently (risky tools stay sequential).
`Agent.CallTool` runs one directly. Built-in tools include time, HTTP, web
fetch, web search, shell, files, line-numbered read, write, edit, sleep,
stop, notebook, todos, python and ask_user.

`WebFetch` is `HTTP` for reading: one GET, and HTML turned into text instead
of markup, so a page does not spend the context window on tags and scripts.
`Todos` keeps the agent's plan in session state, where it survives a pause
and a restart, and refuses a list with two items in progress. `Python` runs
model-written code and is the one built-in whose argument cannot be checked
— an allowlist constrains a command, and Python is a language — so it
requires `Config.Sandbox` (or sealed mode) and refuses to run on the host
unless you pass `AllowUnsafeHost`. The code goes to the interpreter on
stdin: no shell, nothing to quote.

## Streaming

```go
for e, err := range a.Stream(ctx, "Plan a trip") {
    if err != nil { break }
    switch e.Type {
    case kritrim.EventText:       fmt.Print(e.Text)
    case kritrim.EventToolCall:   fmt.Println("->", e.ToolCall.Name)
    case kritrim.EventToolResult: fmt.Println("<-", e.ToolResult.Content)
    case kritrim.EventApproval:   fmt.Println("?", e.Approval.Message)
    }
}
```

Event types are constants: `EventText`, `EventReasoning`, `EventToolCall`,
`EventToolResult`, `EventApproval`, `EventStepStart`, `EventStepEnd`,
`EventFinish`, `EventInterrupt` (with `Event.Interrupts`) and `EventError`.
`Result.Stop` is one of `StopDone`, `StopMaxSteps`, `StopInterrupt`,
`StopStopped`, `StopHook` and `StopGuardrail`. Breaking out early cancels the
run.

## Sessions

```go
a, _ := kritrim.New(kritrim.Config{
    Model:   model,
    Session: kritrim.FileSessions(".agent/sessions"), // or MemorySessions / SQLSessions
})
a.Run(ctx, "My name is Ada.", kritrim.Thread("user-42"))
a.Run(ctx, "What is my name?", kritrim.Thread("user-42"))
```

Sessions are version-checked: two writers racing get `ErrSessionConflict` and
the second can retry. `Agent.Fork` snapshots a conversation (including paused
state) into a new id. `SQLSessions` uses `database/sql`; import your driver.
`RunID` + `Agent.Cancel` stop a run; `Idempotency` deduplicates concurrent
runs with the same key.

## Durable approvals

Synchronous approvals use `Config.Approve`. To pause and resume later:

```go
a, _ := kritrim.New(kritrim.Config{
    Model:   model,
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
    Model:   model,
    Retry:   &kritrim.Retry{Max: 4}, // default: 3 attempts, jittered backoff
    Context: &kritrim.ContextConfig{MaxTokens: 60_000, KeepLast: 30},
    Hooks: kritrim.Hooks{
        BeforeModel: func(ctx context.Context, e *kritrim.ModelEvent) error { return nil },
        AfterTool:   func(ctx context.Context, e *kritrim.ToolEvent) { /* audit/metrics */ },
    },
})
```

### Context

```go
Context: &kritrim.ContextConfig{
    MaxTokens: 60_000,
    KeepLast:  30,
    Strategy:  kritrim.SummarizeWith(small, ""), // or KeepWindow(), Summarize(fn)
},
```

Nothing happens while the history fits. Over `MaxTokens` — measured with
the same exported `kritrim.EstimateTokens` the budget uses — the strategy
decides what the call sends. `KeepWindow` is the default and makes no model
call; `SummarizeWith` pays for one and keeps the gist; `Summarize(fn)` and
the `Strategy` interface take your own. A strategy only changes what is
sent: the session keeps every message, and a cut never orphans a tool
result.

Hooks run after the built-in security checks; they can mutate requests,
responses and tool results but cannot grant a capability policy denied.
They can also steer the loop:

```go
Hooks: kritrim.Hooks{
    AfterModel: func(ctx context.Context, e *kritrim.ModelEvent) {
        if strings.Contains(e.Response.Text, "TODO") { e.Retry = true } // ask again
    },
    BeforeTool: func(ctx context.Context, e *kritrim.ToolEvent) error {
        if e.Call.Name == "web_search" { e.Swap = "internal_search" } // reroute
        return nil
    },
    AfterTool: func(ctx context.Context, e *kritrim.ToolEvent) {
        if e.Result.IsError { e.End = "tool failed" } // Result.Stop == StopHook
    },
}
```

`Retry` is capped by `Retry.Max`; a swapped call goes through approval,
argument validation and `BeforeTool` again, so a swap cannot skip a check.
Combine hook sets with `kritrim.Chain`; they run in order, so list guards
before observers:

```go
Hooks: kritrim.Chain(guard.PII("email"), memory.Inject(store, 5), trace.OTel(otelCfg)),
```

`Context` trims old messages and optionally summarizes dropped history.

Tools and hooks can read per-session state:

```go
kritrim.StateFrom(ctx).Set("seen", true)
```

## Security

- **Default deny.** `HTTP` and `Shell` refuse to be constructed without an
  allowlist. `Files` is read-only unless `Write: true`.
- **SSRF, rebinding and redirects.** Exact allowlisted hosts may be private;
  wildcard hosts must be public, checked at dial time on the connected IP so
  DNS rebinding cannot reach an internal address; every redirect is
  re-validated, and a request carrying a secret never follows one to another
  host.
- **No shell interpretation.** `Shell` takes `argv`; `argv[0]` must be allowed,
  on the host, in a sandbox and inside a sealed box.
- **No inherited credentials.** Shell commands, MCP stdio servers and local
  boxes get a minimal environment (`PATH`, `HOME`, locale, temp dir); add
  variables with `ShellConfig.Env` / `MCPConfig.Env`.
- **Symlink-safe files.** File tools use `os.Root`; a symlink cannot leave the
  root.
- **Validated arguments.** Tool arguments must match the tool schema.
- **Secrets stay out of prompts.** `secret://NAME` is resolved at use and
  redacted from output, events and audit.
- **Approvals.** Risky tools need `Config.Approve`; without it they are refused.
- **Limits.** Steps (20), tool calls (100) and tokens (1,000,000) per run by
  default, per-tool timeout, panic recovery. `Limits.Tokens: -1` removes the
  token cap. The budget is checked before every model call against the
  estimated input, and output is capped to what remains.
- **Tamper-evident audit.** Hash-chained JSON lines; `VerifyAudit` checks them.
  With `AuditKey(key)` the chain is HMAC-SHA256 and a signed head file detects
  truncation, so rewriting the log needs the key. A sink verifies an existing
  log before appending and refuses one it cannot verify.
- **Trusted message history.** `SignedSessions(store, key)` HMACs every
  stored conversation and refuses one that was edited — or one that carries
  no signature at all. Message history is an instruction channel: an agent
  replays whatever the store returns, so write access to a shared Redis or
  a mounted volume is write access to the user's instructions. The
  signature covers content, not freshness: it catches an edit, not a
  deletion or a rollback to an older signed copy.
- **Authorization.** `authz` answers the question `Config.Tools` cannot:
  not "may this agent refund" but "may *this caller* refund *this much*".
- **PII hooks.** `guard.PII("email", "credit_card")` redacts prompts, replies
  and tool results.
- **Tracing.** `trace.JSON(w)` writes one line per run/model/tool event;
  `trace.OTLP(...)` exports OTLP/HTTP traces and `trace.OTel(...)` also
  exports metrics and logs.
- **User Go tools are your code.** The broker governs built-in tools; put
  untrusted work in a sandbox.

## Authorization

```go
policy, err := authz.New(
    authz.Permit("*").For("admin"),
    authz.Permit("current_time", "web_*"),
    authz.Permit("refund").For("support").
        When(func(r authz.Request) bool { return r.Number("amount") <= 100 }).
        Because("support may refund up to 100 euros; ask a manager for more"),
    authz.Forbid("shell"),
)
a, _ := kritrim.New(kritrim.Config{Model: model, Tools: tools, Hooks: policy.Hooks()})
a.Run(ctx, prompt, kritrim.User("u-7"), kritrim.Roles("support"))
```

Cedar's three rules: an explicit `Forbid` beats every `Permit`, one matching
`Permit` is enough, and anything unmatched is denied. Default deny is the
point — adding a tool to an agent does not quietly widen what every caller
can reach, and a typo in a policy fails closed.

Identity comes from the run (`kritrim.User`, `kritrim.Roles`), so one agent
serves many callers. Conditions see the arguments the model chose, which is
where ceilings and path prefixes belong. A denial is a message to the model,
not an error: the run continues and the model explains the limit, using the
`Because` text — a call nothing permitted is told the rules for that tool,
so a caller who went over a ceiling learns what it was.

Policy runs after kritrim's own checks, so it can only narrow what an agent
may do: it cannot grant `Shell` a command its allowlist lacks, and it cannot
waive an approval.

## Sandboxes

### Tools in a sandbox

```go
a, _ := kritrim.New(kritrim.Config{
    Model:   model,
    Tools:   []kritrim.Tool{kritrim.Shell(kritrim.ShellConfig{Allow: []string{"go", "git"}})},
    Sandbox: kritrim.Docker(kritrim.DockerConfig{Image: "golang:1.27"}),
})
```

`Local` exists for development. `sandbox/ssh` runs commands and files on a
remote host with host-key checking and an SSH option allowlist.
Containers kritrim creates drop all capabilities, set `no-new-privileges`,
have no network, and are capped at 2 GB memory, 2 CPUs and 512 processes;
`DockerConfig` has `User`, `ReadOnly`, `Memory`, `CPUs`, `PidsLimit` and
`CapAdd` to tune that. `DockerConfig{Container: "name"}` attaches to an
existing container instead of creating one, and `StreamingMachine` streams
exec output. Every adapter
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
    Model: kritrim.Anthropic("claude-sonnet-5-5"),
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

The host treats the box as untrusted: identity (user, roles, thread, run)
comes from the run the host started, a risky bridged tool runs only for a
call the host approved (once, with exactly the approved arguments), the token
budget (and the run's `Limits.Tokens`) is checked before every model call,
host calls are leased to the run and canceled when it ends, secret
placeholders are random, and audit entries from the box are marked
`source: "box"`.

For containers, build the box with `go run ./cmd/kritrim box build -o box .`
(default `linux/amd64`). `Local` needs no binary and requires
`AllowUnsafeLocal: true`.

### Cloud and microVM sandboxes

```go
box, _ := e2b.New(e2b.Config{})                   // E2B_API_KEY
box, _ := tensorlake.New(tensorlake.Config{})     // TENSORLAKE_API_KEY
box, _ := daytona.New(daytona.Config{})           // DAYTONA_API_KEY
box, _ := microsandbox.New(microsandbox.Config{}) // the msb CLI
box, _ := firecracker.New(firecracker.Config{Kernel: "vmlinux", RootFS: "rootfs.ext4"})
```

| Adapter | Tools in a sandbox | Sealed agent | Network off by default |
|---|:---:|:---:|:---:|
| `sandbox/e2b` | yes | yes | yes |
| `sandbox/tensorlake` | yes | yes | yes |
| `sandbox/microsandbox` | yes | yes | yes (`--no-net`) |
| `sandbox/firecracker` | yes | yes | yes (no NIC) |
| `sandbox/daytona` | yes | no (no process stdio) | yes |

API keys stay on the host. Firecracker needs Linux with KVM and a root
filesystem containing the guest agent: `go run ./cmd/kritrim vmagent build`.
Daytona merges stderr into stdout. Each adapter passes
`sandbox/sandboxtest` against a protocol-level fake and has a live test that
runs when its credentials are set. The contract for your own adapter is
`Sandbox` + `Machine` + `Process`; see [`sandbox/README.md`](sandbox/README.md).

## MCP

```go
a, _ := kritrim.New(kritrim.Config{
    Model: model,
    MCP: []kritrim.MCPConfig{
        {Name: "github", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}},
        {Name: "docs", URL: "https://mcp.example.com/mcp",
            Headers: map[string]string{"Authorization": "Bearer secret://MCP_TOKEN"}},
    },
})
```

stdio and Streamable HTTP are supported, with tool namespacing
(`server__tool`), per-call timeouts, host allowlists and `Risky`. stdio
servers do not inherit your environment (pass `Env`), and HTTP servers never
get a redirect off their host. In sealed
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

Presets build an ordinary graph:

```go
pipeline, _ := multiagent.Sequence(
    multiagent.Step{Name: "draft", Node: multiagent.AgentNode(writer)},
    multiagent.Step{Name: "review", Node: multiagent.AgentNode(reviewer)},
)
router, _ := multiagent.Router(classify, map[string]multiagent.Node{
    "billing": multiagent.AgentNode(billing),
    "tech":    remote.Node(), // an A2A agent
})
```

Swarm members get `handoff_to_<name>` tools. `Graph.AsTool` and
`Swarm.AsTool` hand a whole composition to another agent as one tool, the
way `Agent.AsTool` hands over one agent; the caller's model sees a sentence
and never the nodes. Workflows are plain Go; there is no extra primitive.

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

Agent card, `message/send`, `message/stream`, `tasks/get`, `tasks/cancel`, text,
data and file parts, per-context isolation, and interrupt/resume are
implemented. `c.Run(ctx, "summarize", a2a.Files(file))` sends files; the
server gives images to the model, inlines text files, refuses other types,
and fetches a file URI only from `ServerConfig.FileHosts` (checked at dial
time, no redirects, capped by `MaxFileBytes`). `c.Node()` makes a remote
agent a graph node.
Tasks live in memory or SQL (`a2a.SQLTasks`). Auth is required; contexts are not an auth boundary. The client supports the
OAuth client-credentials grant (`TokenURL`, `ClientID`, `ClientSecret`).
Push notifications: set `ClientConfig.PushURL` (or `Client.SetPush`) and the
server POSTs task updates to that webhook, with an optional notification token.
Push URLs must be public http(s), redirects are not followed, and push header
values are sent as given (`secret://` is never resolved on the server).
`Bearer` compares in constant time and refuses an empty token.
`a2a.Tokens(map[token]principal)` gives each caller its own tasks, contexts
and push configs; other callers get "task not found".

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
LLM judge, detectors, a JSON/Markdown report and a disk cache.

`evals.Judge(criterion, model)` scores a built-in rubric — `Faithful`,
`Correct`, `Relevant`, `Coherent`, `Concise`, `Helpful`, `Harmless`,
`Unbiased`, `Answered`, `Obedient`, `Achieved`, `Recovered` — where 1.0 is
always good, including the safety ones. The criteria are exported rubric
strings, so you can read what is being asked and copy one into
`JudgeConfig.Rubric` to adapt it. Judges see the trajectory, not just the
final wording, so a rubric can grade grounding and completion rather than
confidence. `ToolParameters(tool, check)` checks the arguments a tool was
called with — the half of tool use a trajectory check misses.

`evals.Chaos(ChaosConfig{Fail: map[string]int{"http_request": 1}})` makes
tools fail on purpose, because a suite run on a good day only proves the
agent works on a good day. The failures are counted, not random: the same
case fails the same call every time, so a red result is reproducible.
`Resilient()` is the deterministic half of recovery testing — it finished
despite a failure — and `Judge(Recovered, model)` asks whether what it did
instead made sense. `evals.RunRedTeam`
runs prompt-injection, exfiltration and destructive-tool probes;
`evals.GenerateCases` proposes cases from an agent description and
`evals.Simulate` runs a model-played multi-turn user.

`Experiment.Score` scores results recorded elsewhere, and `evals.FromTrace`
rebuilds them from `trace.JSON(w, trace.Content())` output, so production
traffic can be graded with the same evaluators. The CLI needs no Go code:

```bash
kritrim-evals generate -model anthropic:claude-sonnet-5-5 -context agent.md -n 10 > cases.jsonl
kritrim-evals run -cases cases.jsonl -contains -detect -o report.json -- go run ./myagent
kritrim-evals traces -judge faithful,achieved -judge-model openai:gpt-4.1 trace.jsonl
kritrim-evals report report.json
kritrim-evals diagnose report.json
```

`run` starts the agent program once per case with the input on stdin; it
prints the answer, or a `kritrim.Result` as JSON to keep tool calls for
trajectory checks. Every command exits 1 when a case fails.

## Skills and plugins

```go
sk, _ := skills.Load("./skills")
a, _ := kritrim.New(kritrim.Config{
    Model: model,
    Tools: append(skills.Tools(sk), kritrim.Time()),
    // skills.Prompt(sk) goes into Instructions
    Hooks: plugin.Inject(func(ctx context.Context) string { return "time: " + time.Now().Format(time.RFC3339) }),
})
```

`plugin.Offload(dir, bytes)` moves large tool results to files and returns a
`read_offloaded` tool. `plugin.Steer` adds just-in-time guidance to the next
model call. `guard.Deny(pattern, reason)` blocks matching prompts;
`guard.PII(...)` redacts personal data.

`plugin.GoalLoop` closes the gap between a model that stops and a job that
is done:

```go
res, err := plugin.GoalLoop(ctx, agent, plugin.GoalConfig{
    Goal:   "go build ./... exits zero",
    Thread: "fix-build",
    Done: func(ctx context.Context, res *kritrim.Result) (bool, error) {
        return exec.Command("go", "build", "./...").Run() == nil, nil
    },
})
```

One `Run` ends when the model stops calling tools, which is its judgement
and not a fact. `GoalLoop` re-prompts the same thread while `Done` says
otherwise, up to `MaxAttempts` (default 5), and returns `ErrGoalNotMet`
with the last result when the attempts run out. Check the world, not the
transcript: a model that says the tests pass has only said so. A run that
pauses for an approval comes back as it is — a human has to decide before
anything else can happen.

## Triggers

```go
sched, _ := trigger.NewScheduler(agent,
    trigger.Schedule{Name: "digest", Cron: "0 9 * * mon-fri", Location: berlin, Thread: "daily", Prompt: "summarize"},
    trigger.Schedule{Name: "poll", Every: 10 * time.Minute, Thread: "poll", Prompt: "check the queue"})
sched.SetStore(store) // optional: several instances, each tick runs once
sched.Start(ctx)
defer sched.Close()

hook, _ := trigger.Webhook(trigger.WebhookConfig{Agent: agent, Secret: "secret://HOOK_SECRET"})
http.Handle("/hook", hook)
```

Webhooks require `X-Kritrim-Timestamp` and an HMAC-SHA256
`X-Kritrim-Signature` over timestamp, `Idempotency-Key` and body
(`trigger.Sign` builds it). Stale requests (default 5 minutes) are refused,
each delivery runs once even when duplicates arrive concurrently, a key reused
for a different body gets 409, a full dedupe memory answers 503 instead of
forgetting deliveries, and run errors are logged rather than returned.
Deliveries are remembered in a `DedupeStore`: `MemoryDedupe` for one process,
`SQLDedupe(db, ...)` (Postgres, SQLite, MySQL) when several instances serve
the webhook, so a retry that lands on another instance still runs once.

## Memory

```go
store, _ := memory.NewFile(".agent/memory")
a, _ := kritrim.New(kritrim.Config{Model: model, Tools: memory.Tools(store)})
```

`remember` and `recall` tools backed by a lexical memory store (memory or
file). `memory.Inject(store, n)` recalls relevant facts into the system prompt
before each model call.

```go
store, _ := memory.NewSemantic(base, memory.OpenAIEmbedder("text-embedding-3-small"),
    memory.SemanticConfig{Dir: ".agent/memory"})
```

`NewSemantic` adds embedding search to any store: recall ranks by meaning,
blended with the keyword score, and vectors are kept next to the store so a
restart does not re-embed. Embedders: `OpenAIEmbedder` (and any compatible
API), `OllamaEmbedder`, `GeminiEmbedder`, `BedrockEmbedder`. No vector
database; what you remember is sent to the embedding provider.

## Examples

```bash
go run ./examples/quickstart
go run ./examples/tools
go run ./examples/reasoning   # reasoning, routing, context compaction
go run ./examples/policy      # authorization, signed history, goal loop
go run ./examples/full
go run ./examples/sealed
go run ./examples/a2a
go run ./examples/evals
go run ./examples/multiagent
go run ./examples/semantic-memory
go run ./examples/cron
go run ./examples/a2a-files
```

## Test

```bash
go test ./...
go test -race ./...
go vet ./...
go test -run='^$' -fuzz=FuzzPaths -fuzztime=30s .
go test -run='^$' -bench=. -benchmem .
```

CI also runs `govulncheck`, `staticcheck`, short fuzzing of the path, schema,
network-guard and cron code, a benchmark smoke run, and an API-compatibility
check against the latest v1 release. Numbers are in
[`docs/BENCHMARKS.md`](docs/BENCHMARKS.md).

## Status

v1.0 is stable: core loop, providers (OpenAI-compatible, Anthropic, Gemini,
Bedrock) with reasoning, routing and guardrails, tools, sessions, durable
approvals, hooks, context strategies, audit, authorization, PII, tracing,
Docker/Local/SSH/E2B/Tensorlake/Daytona/microsandbox/Firecracker sandboxes,
sealed mode, MCP, A2A with file parts, evals and its CLI, graphs, swarms and
presets, cron triggers with shared dedupe, lexical and embedding memory,
skills and plugins. See [`CHANGELOG.md`](CHANGELOG.md) and
[`docs/ROADMAP.md`](docs/ROADMAP.md).

## License

MIT; see [`LICENSE`](LICENSE).
