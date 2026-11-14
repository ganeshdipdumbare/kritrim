# API stability

kritrim v1 follows [semantic versioning](https://semver.org). This page says
exactly what that promises.

## Covered

Every exported identifier in these packages:

`kritrim`, `a2a`, `authz`, `evals`, `guard`, `memory`, `multiagent`, `plugin`,
`skills`, `trace`, `trigger`, `sandbox/sandboxtest`, `sandbox/ssh`,
`sandbox/e2b`, `sandbox/daytona`, `sandbox/microsandbox`,
`sandbox/tensorlake`, `sandbox/firecracker`.

Within v1:

- Nothing exported is removed or renamed, and no signature changes.
- Struct fields and interface-satisfying methods are only added. Use keyed
  struct literals (`kritrim.Config{Model: m}`), which is what every example
  does; positional literals of kritrim structs can break on a minor release.
- Interfaces you implement (`Model`, `SessionStore`, `AuditSink`, `Sandbox`,
  `Machine`, `Process`, `Strategy`, `memory.Store`, `memory.Embedder`,
  `trigger.DedupeStore`, `evals.Evaluator`, `evals.Cache`, `a2a.TaskStore`,
  `a2a.Auth`) gain no methods. A new capability arrives as a separate,
  optional interface, the way `StreamingMachine` does.
- String values that appear in constants (`Event*`, `Stop*`, `a2a.Kind*`,
  `a2a.State*`) keep their meaning. New values may be added; handle unknown
  values with a `default` case.
- Defaults that are security controls only get stricter. A minor release may
  refuse a configuration that was unsafe, with an error that says what to
  change, and will say so in the changelog.

## Wire formats

Data kritrim writes and reads back stays readable across v1:

- Stored sessions (`Session` JSON, `FileSessions`, `SQLSessions` tables,
  `SignedSessions` signatures).
- Audit logs and signed heads (`AuditFile`, `VerifyAudit`).
- The sealed box protocol between host and box of the same minor version;
  build the box with the same kritrim version as the host.
- `trace.JSON` lines: fields are added, never renamed.
- `evals.Report` JSON.
- The webhook signature scheme (`kritrim-webhook-v1`).
- A2A follows the A2A protocol version on the agent card.

## Not covered

- `internal/...` packages, `cmd/...` source and `examples/...`.
- The `kritrim`, `kritrim-evals` and `kritrim-vmagent` command-line flags may
  gain options; existing flags keep working.
- Exact error message text. Match errors with `errors.Is` and `errors.As`
  against the exported error values and types.
- Log lines, `Config.Logger` output and benchmark numbers.
- Behaviour that the documentation calls a bug, and fixes to it.
- Provider APIs: when a provider changes or retires an endpoint, the adapter
  follows the provider.

## Go versions

kritrim supports the two most recent Go releases, starting with Go 1.27.
Raising the minimum Go version is done in a minor release and noted in the
changelog.

## Checking

CI runs `gorelease` against the latest v1 tag and fails on an incompatible
change.
