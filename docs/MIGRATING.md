# Migrating from v0.9 to v1.0

Most programs build against v1.0 unchanged. This page lists everything that
can break a build or change behaviour, with the fix for each.

```bash
go get github.com/ganeshdipdumbare/kritrim@v1.0.0
```

## Build changes

### Webhook dedupe memory

`WebhookConfig.MaxEntries` is gone; the dedupe memory is now a store you can
share between instances.

```go
// v0.9
trigger.Webhook(trigger.WebhookConfig{Agent: a, Secret: s, MaxEntries: 50_000})

// v1.0
trigger.Webhook(trigger.WebhookConfig{Agent: a, Secret: s, Store: trigger.MemoryDedupe(50_000)})

// v1.0, several instances behind a load balancer
store, _ := trigger.SQLDedupe(db, trigger.SQLConfig{Dialect: "postgres"})
trigger.Webhook(trigger.WebhookConfig{Agent: a, Secret: s, Store: store})
```

### Guardrails on providers without one

`ModelConfig.Guardrail` is new. Setting it on any provider except Bedrock
fails `kritrim.New`, because a guardrail that is silently ignored is worse
than none. For other providers, use the hook:

```go
hooks, err := guard.Bedrock(guard.BedrockConfig{ID: "gr-123", Version: "1"})
```

## Behaviour changes

### Safety stops

A reply the provider blocked used to end with `Result.Stop == "stop"`. It now
ends with `kritrim.StopGuardrail`, and any tool calls in that reply are not
run. This covers Bedrock `guardrail_intervened`, OpenAI `content_filter`,
Anthropic `refusal` and Gemini `SAFETY`, `PROHIBITED_CONTENT`, `BLOCKLIST`,
`SPII` and `RECITATION`. Code that treats every non-`"stop"` result as a
failure should decide what a guardrail stop means for it.

### Schedules

- `Every` schedules now tick at multiples of the interval since the Unix
  epoch rather than one interval after `Start`. With `Every: time.Hour` the
  first run is at the top of the next hour. This is what lets several
  instances agree on a tick.
- Each schedule has a name (default: its `Thread`), and names must be
  unique. Two schedules on the same thread need distinct `Name`s.

### Webhook retries

A duplicate delivery that arrived while the first run was still going used to
get the first run's answer — including `500 run failed`. It now runs the
delivery itself when the first run fails, which is what a sender retrying
expected. Successful runs are still shared.

### Trace content

`trace.JSON` no longer writes the system prompt by default; nothing it writes
without `trace.Content()` is conversation text. Every line now has `run`, and
tool lines have `call`. To keep the system prompt, and to make traces
scorable with `kritrim-evals traces`:

```go
Hooks: trace.JSON(w, trace.Content())
```

### Interrupt event

A paused run now emits `kritrim.EventInterrupt` (with `Event.Interrupts`)
before `EventFinish`. A `switch` on event types with a `default` that fails
needs a case for it.

### Red-team defaults

`evals.DefaultProbes` has ten probes instead of five, so `RunRedTeam` with no
probes runs more cases. Assertions on the exact number of results change.

### Conformance suite

`sandboxtest.Run` now also checks exit codes, `ExecRequest.Stdin`,
executable file modes, process stdin, and that a started process survives
the end of the context passed to `Start` (`Machine.Start` documents this;
a sealed agent depends on it). A third-party adapter that ignored
any of these fails it; that is the point. Adapters that cannot stream a
process's stdio pass `sandboxtest.Options{NoStart: true}` to `RunWith` and
return `errors.ErrUnsupported` from `Start`.

### Docker stdin

`Docker` sandboxes now forward `ExecRequest.Stdin`. Commands that read stdin
used to receive nothing in a container; they now receive the input.

## New, and worth adopting

- Use the `Event*` and `Stop*` constants instead of string literals.
- `ModelEvent.Retry`, `ModelEvent.End`, `ToolEvent.End` and `ToolEvent.Swap`
  replace hand-rolled retry and stop logic in hooks.
- `kritrim-evals run -- your-agent` evaluates any agent program without Go
  glue code.
