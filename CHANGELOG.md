# Changelog

All notable changes. kritrim follows [semantic versioning](https://semver.org)
from v1.0.0 on; see [docs/API.md](docs/API.md) for what the promise covers and
[docs/MIGRATING.md](docs/MIGRATING.md) for moving from v0.9.

## Unreleased

### Fixed

- **`Local()` is no longer treated as a sandbox.** It runs on the host, so the
  python tool now needs `PythonConfig.AllowUnsafeHost` with it, as the docs
  describe. Previously `Config.Sandbox: Local()` skipped that guard.
- **Sealed boxes cannot read host secrets through `secret://` references** in
  an HTTP request. Boxes get secrets only through the placeholder `secret` RPC.
- **Signed sessions reject a record copied under another thread's key.** The
  signature already covered the record's own ID, but `Load` did not check it
  against the requested ID.
- **SSH sandbox quotes environment values** and refuses entries that are not
  `NAME=value`. A value containing shell metacharacters could run commands on
  the remote host, and values with spaces were split.
- **Resume decisions apply once, to the call they were given for.** A call that
  reused an approved call ID with other arguments used to run unapproved.
- **Audit failures stop the agent.** A failed audit write refuses the next
  model or tool step. Writers share one log under an advisory lock, and the head
  file gets a unique temp name.
- **File tools keep their root with `Local()`.** The Local sandbox no longer
  bypasses the file root jail. Relative sandbox roots are refused.
- **Sealed boxes cannot use `secret://` in HTTP requests**, and the box's
  request is refused rather than resolved from the host.
- **Output from sandboxes is capped** (8 MiB per stream), and streamed output
  keeps long lines whole instead of dropping them past 64 KiB.
- **Docker env values stay off the command line.** Names go on argv, and values
  go in the docker client's environment. Docker stderr has the values removed.
  Docker requests honor `ExecRequest.Timeout`.
- **Docker and microsandbox refuse names that start with `-`** for images,
  containers and capabilities.
- **SSH sandbox sets the file mode on write** and refuses more local-code
  options (`PKCS11Provider`, `SecurityKeyProvider`, `Match`, `Include`).
- **MCP**: secret headers are resolved per request, and the caller's header map
  is never written to. Responses are capped at 8 MiB, a too-long SSE frame is an
  error, and the session ID is guarded for parallel calls.
- **A2A server**: the message is validated before an agent is created. Store
  errors fail closed. One run per task at a time. Agents in use are not
  evicted, and a principal evicts only its own idle contexts. Internal errors
  are logged, and clients get a generic message.
- **Signed sessions and file sessions**: `SignedSessions.Load` refuses a record
  under another thread's key. `FileSessions.Save` is serialized by a directory
  lock, so concurrent saves cannot both win.
- **SigV4** signs the canonical query string with RFC 3986 encoding and keeps
  repeated keys, so requests with spaces or repeated parameters verify.
- **Dedupe and webhooks**: an expired unfinished claim can be taken over. Runs
  have a timeout (`WebhookConfig.RunTimeout`, default 10 minutes). Only an
  over-size body is a 413. A failed read is a 400.
- **OTLP tracing** no longer leaks a span entry for runs without a run ID.
  Those runs are not traced.
- **vmagent unix socket** is owner-only, and a live socket is not unlinked.
- **CI** pins its actions to commit SHAs and its Go tools to versions.

- **Sealed boxes run the host's hooks on bridged calls.** Policy hooks (authz,
  guard) see a call the box makes. A hook may deny it but may not change its
  arguments. Approval requests are capped at 20 per run, their text is
  sanitized, and the `secret` RPC needs an active run.
- **A risky call that a hook changes after approval is blocked**, in place or
  by replacement.
- **Runs checkpoint as they go.** The session is saved after each tool result,
  so a retry after a failure does not run calls that already ran.
- **Idempotency keys are scoped to the caller's identity and thread**, and an
  in-flight run's key never expires.
- **MemorySessions copies nested data.** Editing a loaded session no longer
  edits the stored one.
- **Audit fields are capped at 512 KiB**, so the writer never accepts an entry
  the reader cannot parse.
- **Dedupe release and complete are fenced by the claim's expiry**, so a stale
  leader cannot delete a newer claim. `DedupeTTL` is at least
  `RunTimeout + 2*Tolerance`.
- **A2A**: one run per context as well as per task; a per-principal cap on runs
  in flight (`ServerConfig.MaxRunsPerPrincipal`, default 8); task updates are
  serialized; history and artifacts are capped per task.
- **vmagent**: replies and reads are capped, a timeout kills the whole process
  group, a panic ends only its connection, `start` checks its pipes, and only
  the host CID may connect over vsock. Accept errors are retried, and the
  non-unix listener fails closed.
- **Local sandbox**: `ExecStream` and `Start` enforce `Allow` (`StartRequest`
  gained an `Allow` field).
- **Docker** refuses `DOCKER_*` names in sandbox env. **SSH** refuses a Host or
  IdentityFile that starts with `-`.
- **kritrim-evals** kills a case's whole process group on timeout.

### Added

- `ExecRequest.Allow`: when set, the Local machine refuses commands outside it.
  The agent always sets it.
- `WebhookConfig.RunTimeout`.

### Known limits

- The audit head can lag the log by one entry after a crash. One trailing entry
  can be removed undetected in that window.
- Commands are matched by name on the host PATH. Pin absolute paths in the
  allowlist to avoid a PATH substitution.
- Docker `Start` is long-lived, so `ExecRequest.Timeout` does not bound it.
- A direct `Machine` caller that passes no `Allow` is unrestricted on the Local
  machine. The agent always sets `Allow`. Other machines ignore it.
- vmagent authenticates the peer by CID only. A boot-time secret is not yet
  provisioned by the host.

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
