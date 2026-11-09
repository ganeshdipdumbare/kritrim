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
path; double resume conflict; tool runs once; retry and fallback tests.

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

**Still open:** SQL task store, push notifications, file parts, OAuth/SigV4.

### Evals (`evals/`, `cmd/kritrim-evals`)
- `Case`, `Experiment`, `Report`, bounded concurrency, `DirCache`.
- Evaluators: exact, contains, regex, JSON fields, tool calls, no-error,
  max-steps, max-tokens, max-duration, LLM judge with rubric.
- Detectors: run error, empty output, max steps, pending approval, tool error,
  approval rejected, repeated call loop.
- CLI: `report` (exit 1 on failure), `diagnose`.

**Gate:** deterministic evaluator fixtures; judge with Mock; cache hit;
concurrency; report rendering.

**Still open:** user/tool simulators, red-team suite, case generation.

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

**Still open:** E2B/microsandbox/Firecracker/Tensorlake/Daytona adapters; A2A
SQL tasks; SSH adapter live test (skipped without a host).

## v0.5 — multi-agent + triggers + memory (shipped)

- Graph: named nodes, conditional edges, shared `State`, step cap, interrupt
  propagation.
- Swarm: members, `handoff_to_<name>` tools, max handoffs, shared task prompt.
- Triggers: interval scheduler (non-overlapping ticks) and HMAC webhook with
  idempotency keys.
- Memory: memory/file stores and `remember`/`recall` tools.

**Gate:** graph branch/cycle tests; swarm handoff test; scheduler tick test;
webhook signature + duplicate test; memory tool test.

**Still open:** cron expressions with named schedules, SQL trigger idempotency,
A2A agents as graph nodes (wrap `client.Run`), workflow presets.

## v0.6 — safety, observability, models (partly shipped)

Shipped:
- `guard.PII` hooks (email, phone, card, SSN, IPv4).
- `trace.JSON` lifecycle hooks and `trace.OTLP` OTLP/HTTP trace export.
- `Config.Fallback` model routing on retryable failures.
- Helpers for DeepSeek, Groq, Mistral, OpenRouter, LiteLLM.
- Evals red-team probes (injection, exfiltration, destructive tools).

**Still open:** Bedrock and OpenAI Responses providers, prompt caching,
Cedar-style authorization, steering, notebook/handoff/python vended tools,
voice/bidi.

## v1.0 — freeze

API stability, migration guide, benchmarks, docs site, signed releases.

## Non-goals

Voice/bidi, hosted evaluation platform, deploy guides/infrastructure,
experimental JSON agent config, a separate Workflow primitive. They are
separate products or just Go code.
