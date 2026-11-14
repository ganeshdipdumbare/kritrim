# kritrim roadmap

Status as of this commit. Each release lists what shipped and the gate that
proves it. The rule stays the same: one way per task, security on by default.

## v0.1 — core (shipped)

Loop, typed tools, streaming, structured output, providers (OpenAI-compatible,
Anthropic, Gemini, Mock), memory/file sessions, broker, limits, approvals,
hash-chained audit, Docker/Local sandboxes, sealed whole-agent mode.

**Gate:** offline `go test ./...`; `examples/sealed` starts a real box.

## v0.2 — production gate (shipped)

- Durable approvals: `Result.Stop == "interrupt"`, `Session.Pending`,
  `Session.Paused`, resume with `Run(ctx, "", Thread(id), Decisions(...))`.
- Session versioning: compare-and-swap `Save`, `ErrSessionConflict`.
- Retry: jittered backoff on 429/5xx/network, no retry after a streamed delta.
- Hooks: `BeforeRun/AfterRun`, `BeforeModel/AfterModel`, `BeforeTool/AfterTool`.
- Context: `ContextConfig` dropping old messages and optional summarization.
- State: `StateFrom(ctx)` persisted with the session.
- Agents-as-tools: `Agent.AsTool`.

**Gate:** pause in one agent, resume in another over `FileSessions`; reject
path; double resume conflict; tool runs once; retry and fallback tests; sealed
pause and resume across a real box restart (`internal/boxtest`).

## v0.3 — A2A + evals (shipped)

### A2A (`a2a/`)
- Client: agent card, `message/send`, `message/stream` (SSE), lazy card cache,
  headers with `secret://`, host allowlist, `AsTool`.
- Server: card endpoints, JSON-RPC `message/send`, `message/stream`,
  `tasks/get`, `tasks/cancel`, per-context agent LRU, bearer auth, task store
  (memory), data parts, interrupt → `input-required` → resume.
- Interrupts cross the protocol and resume with the same decision IDs.

**Gate:** in-process client↔server card/send/stream; interrupt round-trip;
auth rejection; context isolation.

**Open at the time (shipped in v1.0):** file parts. SQL task store is available via `a2a.SQLTasks`.

### Evals (`evals/`, `cmd/kritrim-evals`)
- `Case`, `Experiment`, `Report`, bounded concurrency, `DirCache`.
- Evaluators: exact, contains, regex, JSON fields, tool calls, no-error,
  max-steps, max-tokens, max-duration, LLM judge with rubric.
- Detectors: run error, empty output, max steps, pending approval, tool error,
  approval rejected, repeated call loop.
- CLI: `report` (exit 1 on failure), `diagnose`.
- `GenerateCases` from an agent description and `Simulate` multi-turn user
  sessions.
- `RunRedTeam` probes for injection, exfiltration and destructive tools.

**Gate:** deterministic evaluator fixtures; judge with Mock; cache hit;
concurrency; report rendering; generator/simulator/red-team tests.

**Open at the time (shipped in v1.0):** richer red-team suites and remote-trace evaluation.

## v0.4 — connect + sandboxes (shipped)

- MCP: stdio and Streamable HTTP (JSON or SSE response), `initialize`,
  `tools/list`, `tools/call`, namespaced tools, timeouts, HTTP allowlist,
  `secret://` headers, `Risky`; host MCP tools auto-bridge into sealed boxes.
- SQL sessions: `database/sql` store with version CAS, Postgres/SQLite/MySQL
  dialect switch.
- SSH sandbox: remote exec/files/stdio, strict host keys, option denylist.
- Conformance kit `sandbox/sandboxtest` for every adapter.
- Box cross-compile: `Sealed.Binary` is required for container sandboxes,
  `Sealed.Platform` drives the error, `kritrim box build` wraps `go build`.
- Vended tools: line-numbered `read`, `write`, `edit`, `sleep`, `stop`.
- Sealed durable approvals: pending/paused state crosses the RPC boundary and
  is stored on the host.

**Gate:** MCP stdio helper + HTTP server tests; Docker/Local conformance;
`go run ./cmd/kritrim box build`; sealed example.

**Open at the time (shipped in v1.0):** E2B/microsandbox/Firecracker/Tensorlake/Daytona adapters; SSH
adapter live test (skipped without a host).

## v0.5 — multi-agent + triggers + memory (shipped)

- Graph: named nodes, conditional edges, shared `State`, step cap, interrupt
  propagation.
- Swarm: members, `handoff_to_<name>` tools, max handoffs, shared task prompt.
- Triggers: interval scheduler (non-overlapping ticks) and HMAC webhook with
  idempotency keys.
- Memory: memory/file stores and `remember`/`recall` tools.

**Gate:** graph branch/cycle tests; swarm handoff test; scheduler tick test;
webhook signature + duplicate test; memory tool test.

**Open at the time (shipped in v1.0):** cron expressions with named schedules, SQL trigger idempotency,
A2A agents as graph nodes (wrap `client.Run`), workflow presets.

## v0.6 — safety, observability, models (shipped)

Shipped:
- `guard.PII` hooks (email, phone, card, SSN, IPv4).
- `trace.JSON` lifecycle hooks and `trace.OTLP` OTLP/HTTP trace export.
- `Config.Fallback` model routing on retryable failures.
- Helpers for DeepSeek, Groq, Mistral, OpenRouter, LiteLLM.
- Evals red-team probes (injection, exfiltration, destructive tools).
- Evals case generation and multi-turn simulation.
- Sealed durable approval test across a box restart.

**Open at the time (shipped in v1.0):** E2B/microsandbox/Firecracker/Tensorlake sandbox adapters, A2A
file parts, evals CLI run/generate and remote traces. (Authorization and
python tools shipped in v0.9; voice/bidi is a non-goal.)

## v0.7 — parity pass (shipped)

- Tool executors: `Config.ToolConcurrency` runs parallel tool calls; risky
  tools stay sequential. `Agent.CallTool` runs a tool directly.
- Lifecycle: `RunID` + `Agent.Cancel`, `Idempotency`, `Agent.Fork` session
  snapshots, `Agent.Session` reads.
- Dynamic tools: `NewRawTool` for MCP-style schemas and simulators.
- Skills: `skills.Load`, `skills.Prompt`, `skills.Tools` (`use_skill`).
- Plugins: `plugin.Inject` (system-prompt context) and `plugin.Offload`
  (large tool results to files) plus `read_offloaded`.
- Guard: `guard.Deny` prompt policy on top of `guard.PII`.
- Models: Amazon Bedrock Converse with SigV4; prompt caching for Anthropic
  (`Config.PromptCache`).
- Sandboxes: Docker attach to an existing container; `StreamingMachine`
  (Docker/Local/SSH) streams exec output.
- Evals: `SimulatedTools` fixture tools.
- A2A OAuth client credentials and push notifications (webhooks, tokens,
  SSRF guard); `Notebook` and `AskUser` tools.
- OpenAI Responses API (`OpenAIResponses`): input/output items, function calls,
  streaming, JSON schema output.
- OTel metrics and logs (`trace.OTel`): model/tool calls, tokens, durations,
  run counts, lifecycle logs; `trace.OTLP` remains traces only.
- Memory injection (`memory.Inject`) and steering (`plugin.Steer`).

**Gate:** parallel timing test; direct call; fork conflict; cancel; idempotency
single model call; skills/offload/inject/deny tests; Bedrock request/signature
tests; prompt-cache body test; Docker attach and streamed-exec tests; A2A
push webhook test; OpenAI Responses text/tool/stream/schema tests; OTel
traces/metrics/logs export test.

## v0.8 — hardening pass (shipped)

- Sealed host treats the box as untrusted: risky bridged tools need a
  host-recorded approval bound to call id and arguments; identity comes from
  the host's run; box audit entries are marked `source: "box"`; the token
  budget is checked before each call; secret placeholders are random; the box
  binary goes to an unguessable path (a private temp dir on Local).
- File tools use `os.Root` (no symlink escape); the shell allowlist is
  enforced everywhere; commands and MCP stdio servers get a minimal env.
- `internal/netguard`: dial-time SSRF checks (rebinding), wider private
  ranges (CGNAT, NAT64, 6to4, reserved); secrets never follow cross-host
  redirects; HTTP methods and schemes are restricted.
- Tool arguments are validated against the tool schema.
- `AuditKey`: HMAC-chained audit with a signed head (truncation detected).
- Webhooks: timestamped signature over key and body, replay window,
  single-flight dedupe with TTL, no error leakage; `trigger.Sign`.
- Docker: cap-drop ALL, no-new-privileges, memory/CPU/pids caps, optional
  user and read-only root.
- A2A: constant-time bearer, empty token refused, push headers never resolve
  `secret://`, push URLs public-only with no redirects, client redirects
  pinned to the host.
- `kritrim.Chain` composes hooks; default per-run token cap
  (`DefaultTokenLimit`).
- CI: govulncheck, staticcheck, short fuzzing, coverage.

**Gate:** `hardening_test.go`, netguard and jsonschema tests and fuzzers,
A2A push/bearer tests, webhook replay and concurrency tests, Docker argument
test.

Review follow-ups (same release): A2A per-principal ownership (`a2a.Tokens`,
`Principal`) for tasks, contexts and push configs; push validation on every
write path; unguardable custom transports refused; https→http redirects never
carry secrets; length-prefixed webhook signatures, key/body fingerprints,
namespaced dedupe ids, no eviction of replayable or in-flight deliveries,
panic-safe completion; audit sinks verify before appending and recover after
failures, empty keys rejected; pre-call token budgets (including repair and
sealed calls, plus the run limit on the sealed host); run-scoped leases for
sealed capability calls; JSON Schema constraints, combinators and local `$ref`.

**Open at the time (shipped in v1.0):** pluggable webhook dedupe store for multi-instance
deployments; richer hooks (retry requests, tool swap, end turn).

## v0.9 — parity pass 2 (shipped)

A pass over the gaps between kritrim and the Strands Harness SDK, taking
the capabilities that fit "one way per task, security on by default" and
leaving the rest in Non-goals below.

- **Reasoning:** `Config.Reasoning{Effort, Budget, Include}` maps onto
  Anthropic extended thinking, OpenAI `reasoning_effort`, Responses
  `reasoning.effort`, Gemini `thinkingConfig` and Bedrock
  `reasoning_config`. Thinking tokens are budgeted as output tokens, and
  Anthropic thinking blocks are kept on the assistant message
  (`Message.Reasoning`, `Message.ReasoningSig`) and replayed with their
  signature, which the provider requires before it will accept a tool
  result.
- **Routing:** `Config.Route` picks the model per run from the prompt and
  the context. `Fallback` still applies to what it chose, and the audit log
  records the model the run used.
- **Context strategies:** `ContextConfig.Strategy` with `KeepWindow`
  (default), `Summarize(fn)` and `SummarizeWith(model, instructions)`, plus
  the exported `EstimateTokens`. Replaces `ContextConfig.Summarize`, which
  is now `Strategy: kritrim.Summarize(fn)`.
- **Trusted message history:** `SignedSessions(store, key)` HMACs stored
  conversations and refuses an edited or unsigned one (`ErrSessionTampered`).
- **Authorization (`authz/`):** Cedar-semantics policies over the run's
  identity and the call's arguments — `Permit`/`Forbid`, `For`, `By`,
  `When`, `Because`, explicit-deny precedence, default deny, and a denial
  that explains the rule it fell outside.
- **Vended tools:** `WebFetch` (HTML to text), `Todos` (plan in session
  state, one item in progress), `Python` (sandbox-required, code on stdin).
  `env.sandboxed()` lets a tool refuse to run on the host.
- **Goal loop:** `plugin.GoalLoop` re-prompts one thread until a goal check
  passes, returning `ErrGoalNotMet` with the last result; `plugin.Goal`
  keeps the goal in the system prompt.
- **Multi-agent as tools:** `Graph.AsTool` and `Swarm.AsTool`.
- **Evals:** `Judge(criterion, model)` over twelve exported rubrics where
  1.0 is always good; judges see the trajectory; `ToolParameters`;
  `Chaos` (counted, reproducible tool failures), `Resilient` and
  `FailedTools`.

**Gate:** per-provider reasoning wire-format tests, signature replay,
reasoning-vs-budget refusal; routing; strategy, estimation and
valid-conversation tests; six tamper cases, wrong key, moved thread and a
run refusing an edited history; authorization matrix, panicking condition,
per-run identity; web-fetch HTML/JSON/404/off-allowlist, todos state round
trip, python sandbox refusal; goal-loop retry, give-up, interrupt;
graph/swarm as tools; criteria, trajectory prompt, chaos determinism.

**Still open (all shipped in v1.0):** provider guardrails (Bedrock
`ApplyGuardrail`), embedding-backed memory search, richer hooks (retry
requests, tool swap, end turn), evals CLI `run`/`generate`, pluggable webhook
dedupe store.

## v1.0 — stable (shipped)

Every "still open" item above, then the freeze.

- **Hooks:** `ModelEvent.Retry`, `ModelEvent.End`, `ToolEvent.End`,
  `ToolEvent.Swap` (re-enters approval and validation); `Event*` and
  `Stop*` constants; `EventInterrupt` is emitted.
- **Guardrails:** `ModelConfig.Guardrail` (Bedrock Converse) and
  `guard.Bedrock` (ApplyGuardrail for any model); provider safety stops end
  with `StopGuardrail`.
- **A2A:** file parts with a fetch allowlist; `Client.Node` for graphs.
- **Multi-agent:** `Sequence`, `Router`, `AgentNode` presets.
- **Memory:** `NewSemantic` with OpenAI, Ollama, Gemini and Bedrock
  embedders.
- **Triggers:** cron and named schedules; `DedupeStore` with `MemoryDedupe`
  and `SQLDedupe` for webhooks and ticks across instances.
- **Evals:** `Experiment.Score`, `FromTrace`, more red-team probes; CLI
  `generate`, `run`, `traces`.
- **Sandboxes:** E2B, Tensorlake, microsandbox, Firecracker (with
  `kritrim-vmagent`) and Daytona adapters; stricter conformance (stdin, exit
  codes, modes, start-context lifetime).
- **Freeze:** MIT license, `docs/API.md`, `CHANGELOG.md`,
  `docs/MIGRATING.md`, `docs/BENCHMARKS.md`, signed release artifacts with
  provenance, `gorelease` in CI, docs site for v1.

**Gate:** hook retry/end/swap tests (swap still needs approval, no double
swap); guardrail wire and refusal tests; ApplyGuardrail block/mask/fail-closed
tests; A2A file mapping, refusal, allowlist, redirect and private-address
tests; preset and remote-node tests; semantic ranking, persistence and
embedder wire tests; cron table and fuzz tests; dedupe store contract on
memory and SQL, cross-instance webhook and scheduler tests; trace round trip
and CLI tests; every adapter through `sandboxtest` against a protocol fake;
sealed agents end to end on Docker and E2B; benchmarks; cross-OS vet; all
release targets build.

Found and fixed on the way: a sealed box on Docker was killed when `New`
returned, and Docker `Exec` dropped stdin.

## After 1.0

Additive work that keeps the v1 API: more provider guardrails as providers
expose them, more embedders, adapters for further sandbox providers, and
live-test runs of the cloud adapters in CI once credentials are available
to it.

## Non-goals

Voice/bidi, hosted evaluation platform, deploy guides/infrastructure,
experimental JSON agent config, a separate Workflow primitive. They are
separate products or just Go code.

Considered during the v0.9 parity pass and deliberately left out:

- **Workflow primitive.** A graph with conditional edges and shared state
  already expresses a DAG of steps. A second primitive would be a second
  way to say the same thing.
- **Deployment guides.** A kritrim agent is a Go binary. Lambda, Fargate,
  EKS and Kubernetes guides belong to whoever runs the binary.
- **Declarative agent config.** A `Config` literal is already the
  declaration, and it type-checks.
- **Voice/bidi.** A different transport and a different loop; a separate
  product, not a flag on this one.
- **A provider catalogue.** `Compatible(provider, baseURL, model)` covers
  any OpenAI-shaped endpoint, which is most of them. Named helpers exist
  only where the wire format differs.
