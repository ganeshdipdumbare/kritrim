# Changelog

All notable changes. kritrim follows [semantic versioning](https://semver.org)
from v1.0.0 on; see [docs/API.md](docs/API.md) for what the promise covers and
[docs/MIGRATING.md](docs/MIGRATING.md) for moving from v0.9.

## v1.0.0 — 2026-11-14

The stable release. Every item the roadmap listed as open is done, the API is
frozen, and releases are signed.

### Added

- **Named constants** for event types (`EventText`, `EventToolCall`, …,
  `EventInterrupt`) and stop reasons (`StopDone`, `StopMaxSteps`,
  `StopInterrupt`, `StopStopped`, `StopHook`, `StopGuardrail`,
  `StopToolCalls`, `StopLength`). `EventInterrupt` is now emitted, with the
  pending interrupts, when a run pauses.
- **Richer hooks:** `ModelEvent.Retry` calls the model again (capped by
  `Retry.Max`), `ModelEvent.End` and `ToolEvent.End` end the run with
  `StopHook`, and `ToolEvent.Swap` reroutes a call to another tool — through
  approval, validation and `BeforeTool` again, so a swap cannot skip a check.
- **Provider guardrails:** `ModelConfig.Guardrail` applies a Bedrock guardrail
  inside Converse; `guard.Bedrock` checks prompts, replies and (optionally)
  tool results of any model with ApplyGuardrail and fails closed. Provider
  safety stops (Bedrock `guardrail_intervened`, OpenAI `content_filter`,
  Anthropic `refusal`, Gemini `SAFETY`…) end a run with `StopGuardrail` and
  drop that reply's tool calls.
- **A2A file parts:** `a2a.File`, `FilePart`, `FileURI` and the `a2a.Files`
  run option. The server passes images to the model, inlines text files and
  refuses other types; URIs are fetched only from `ServerConfig.FileHosts`,
  checked at dial time, without redirects, within `MaxFileBytes`.
- **A2A agents as graph nodes:** `a2a.Client.Node`.
- **Workflow presets** that build an ordinary graph: `multiagent.Sequence`,
  `multiagent.Router`, and `multiagent.AgentNode`.
- **Embedding memory:** `memory.NewSemantic` wraps any store with cosine
  search blended with the keyword score; embedders for OpenAI (and any
  compatible API), Ollama, Gemini and Bedrock (Titan, Cohere).
- **Cron schedules:** `Schedule.Cron` (five fields and `@daily`-style
  descriptors), `Schedule.Location`, named schedules, and `trigger.ParseCron`.
- **Pluggable dedupe:** `trigger.DedupeStore` with `MemoryDedupe` and
  `SQLDedupe` (Postgres, SQLite, MySQL). Webhooks share deliveries and
  schedulers share ticks across instances (`Scheduler.SetStore`).
- **Evals:** `Experiment.Score` scores recorded results; `evals.FromTrace`
  rebuilds them from `trace.JSON` output; `CriterionByName`; five more
  red-team probes (indirect injection, secret placeholders, approval bypass,
  file exfiltration). `kritrim-evals` gains `generate`, `run` (any agent
  program, one process per case) and `traces`.
- **Trace content:** `trace.JSON(w, trace.Content())` records prompts,
  replies, tool arguments and results; every line now carries the run id and
  tool lines the call id.
- **Sandbox adapters:** `sandbox/e2b`, `sandbox/tensorlake`,
  `sandbox/microsandbox` and `sandbox/firecracker` (with the new
  `kritrim-vmagent` guest agent and `kritrim vmagent build`) can host a
  sealed agent; `sandbox/daytona` serves `Config.Sandbox`. Each passes the
  conformance suite against a protocol-level fake and has a live test that
  runs with credentials.
- **Conformance:** `sandboxtest.RunWith` and `Options{Dir, NoStart}`; the
  suite now checks exit codes, stdin, executable file modes and process
  stdin.
- MIT license, `docs/API.md` (compatibility promise), this changelog,
  `docs/MIGRATING.md`, `docs/BENCHMARKS.md`, signed release artifacts with
  build provenance, and an API-compatibility check in CI.

### Changed

- `trigger.WebhookConfig.MaxEntries` is replaced by
  `Store: trigger.MemoryDedupe(n)`.
- `Every` schedules tick at multiples of the interval since the Unix epoch;
  schedule names (default: the thread) must be unique.
- A duplicate webhook delivery that was waiting on a failed run now runs the
  delivery itself instead of answering 500.
- `trace.JSON` writes the system prompt only with `trace.Content()`.
- `ModelConfig.Guardrail` on a provider without one fails `kritrim.New`.

### Fixed

- Sealed agents on Docker: the box process was bound to the context `New`
  started it with and was killed as soon as `New` returned, so the first run
  failed with "connection closed". `Machine.Start`'s context now bounds only
  starting the process; the conformance suite checks it and a sealed run in a
  real container is tested.
- Docker `Exec` now forwards `ExecRequest.Stdin` (`docker exec -i`); commands
  that read stdin, including the `Python` tool, received nothing before.
- A sealed agent on a sandbox without process stdio fails with an
  explanation instead of a bare start error.

## v0.9.0 — 2026-11-14

Reasoning across providers, per-run model routing, context strategies,
signed sessions, the `authz` package, web fetch, todos and Python tools, the
goal loop, graphs and swarms as tools, and evaluation criteria with chaos
testing.

## v0.8.0 — 2026-11-13

Security hardening: the sealed host treats the box as untrusted, file tools
use `os.Root`, minimal environments, dial-time SSRF checks, schema-validated
tool arguments, HMAC-chained audit with a signed head, timestamped webhook
signatures, hardened Docker defaults, A2A per-principal ownership, and
govulncheck, staticcheck and fuzzing in CI.

## v0.7.0 — 2026-11-09

Parallel and direct tool calls, run ids with cancel, idempotency, fork,
dynamic tools, skills, inject/offload/steer plugins, Bedrock Converse,
Anthropic prompt caching, Docker attach and streamed exec, A2A OAuth and push
notifications, the OpenAI Responses API, and OpenTelemetry metrics and logs.

## v0.6 and earlier

v0.6 added PII hooks, OTLP tracing, fallback models and red-team probes;
v0.5 graphs, swarms, triggers and memory; v0.4 MCP, SQL sessions and the SSH
sandbox; v0.3 A2A and evals; v0.2 durable approvals, session versioning,
retry, hooks and context; v0.1 the core loop, tools, providers, broker,
audit and the sealed sandbox.
