# Benchmarks

These measure kritrim's own overhead: the loop, checks, stores and helpers,
with the offline `Mock` model, so no provider latency is included. A real
model call takes hundreds of milliseconds to seconds; the numbers below are
what kritrim adds on top.

Run them yourself:

```bash
go test -run='^$' -bench=. -benchmem . ./internal/netguard ./internal/jsonschema ./trigger ./memory
```

Measured for v1.0.0 on an Apple M3 Pro (darwin/arm64), Go 1.27.3.

## The loop

| Benchmark | Time | Memory | Allocs |
|---|---:|---:|---:|
| `Run`, one reply, no tools | 1.1 µs | 2.0 KB | 31 |
| `Run`, five tool calls and a reply | 32 µs | 11.6 KB | 237 |
| `Stream`, one reply | 4.7 µs | 12.1 KB | 42 |
| `Run` with a hook | 1.1 µs | 2.0 KB | 32 |
| `Run` on a `FileSessions` thread | 159 µs | 4.8 KB | 60 |

The five-tool run covers two model calls, argument validation for each
call, and five tool results going back to the model. The file session cost
is the atomic, synced write of the session file.

## Checks

| Benchmark | Time | Memory | Allocs |
|---|---:|---:|---:|
| Tool argument validation (4 fields, enum, pattern) | 2.9 µs | 304 B | 14 |
| JSON Schema value check | 2.5 µs | 192 B | 12 |
| `netguard.Public`, four addresses | 228 ns | 0 | 0 |
| `EstimateTokens`, 100 messages | 336 ns | 0 | 0 |

## Stores and logs

| Benchmark | Time | Memory | Allocs |
|---|---:|---:|---:|
| Audit write, SHA-256 chain | 3.8 ms | 3.2 KB | 16 |
| Audit write, HMAC chain and signed head | 3.5 ms | 5.3 KB | 41 |
| `VerifyAudit`, 1000 entries | 1.5 ms | 1.2 MB | 9030 |
| `SignedSessions` save and load, 20 messages | 10 µs | 10 KB | 29 |

An audit write is dominated by `fsync`: every entry is on disk before the
call it records returns, so a crash cannot lose the record of a tool call
that happened. On Linux with an NVMe disk an fsync is typically well under a
millisecond; on macOS it is slower. If you need a faster sink, implement
`AuditSink` over a log pipeline that gives you the durability you want.

## Triggers and memory

| Benchmark | Time | Memory | Allocs |
|---|---:|---:|---:|
| `Cron.Next`, weekday working hours | 400 ns | 0 | 0 |
| Semantic memory search, 1000 memories | 0.6 ms | 305 KB | 2033 |

Semantic search embeds the query (here with an in-process embedder) and
scores every memory; with a hosted embedder the network call dominates.
