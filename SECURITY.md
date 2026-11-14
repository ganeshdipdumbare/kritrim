# Security

kritrim is a security-first SDK. Please report vulnerabilities privately.

## Reporting

Email **ganeshdip.dumbare@gmail.com** with:

- the affected version or commit,
- a minimal reproduction,
- the impact you believe it has.

Do not open a public issue for a vulnerability. You will get an acknowledgement
within 3 working days and a fix or mitigation plan within 10.

## Supported versions

Security fixes go to the latest v1 minor release. Upgrade within v1 to get
them; the [compatibility promise](docs/API.md) means that is always safe to
do. Pre-1.0 releases (v0.x) are not supported.

## Verifying a release

The Go module is verified by the Go checksum database (`sum.golang.org`)
whenever you run `go get`; nothing else is needed for the library.

The release binaries (`kritrim`, `kritrim-evals`, `kritrim-vmagent`) come
with a `SHA256SUMS` file signed keyless with Sigstore from the release
workflow, and with GitHub build provenance for every file:

```bash
# The checksum file was signed by this repository's release workflow.
cosign verify-blob SHA256SUMS \
  --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github.com/ganeshdipdumbare/kritrim/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# The binary you downloaded is the one in the checksum file.
sha256sum --check --ignore-missing SHA256SUMS

# Or check its build provenance directly.
gh attestation verify kritrim-evals_v1.0.0_linux_amd64 --repo ganeshdipdumbare/kritrim
```

## Scope

In scope:

- the broker and capability checks (`env.go`, built-in tools),
- secret resolution and redaction,
- SSRF and redirect handling,
- the sealed sandbox protocol and host-side broker (`sealed*.go`, `rpc.go`),
- session versioning and durable approvals,
- audit hash chaining,
- A2A auth, push notifications and task handling,
- webhook trigger signatures and replay protection,
- tool argument validation and file-root confinement,
- MCP transports and tool bridging,
- sandbox adapters (`sandbox/*`) and the Firecracker guest agent
  (`cmd/kritrim-vmagent`),
- A2A file parts and URI fetching,
- guardrail hooks (`guard`) and embedding memory requests (`memory`),
- the webhook and schedule dedupe stores (`trigger`),
- the release workflow and its signatures.

Out of scope:

- vulnerabilities in a model provider, cloud sandbox provider or MCP server,
- code you compile into your own binary (your Go tools are trusted code),
- a sandbox provider that fails to isolate its own machine; report that to the
  provider,
- prompt-injection outcomes without a bypass of a documented control.

## Security model, in one paragraph

Built-in tools reach the network, files and processes through one broker and
only with the allowlists their configuration declares. Secrets are resolved at
the point of use and redacted from output, events and audit. A sealed agent
runs the loop and its local tools in a sandbox with no API key, no secret value,
no host filesystem and no network by default; model calls, secrets, egress,
approvals, audit and bridged tools go through the host. The host treats the box
as untrusted: identity, approvals and the token budget are enforced on the
host side. Commands and MCP servers the agent starts do not inherit the host's
environment, file tools cannot follow symlinks out of their root, tool
arguments are validated against their schema, and SSRF checks run at dial
time. With `AuditKey`, the audit log is HMAC-chained and its head is signed.
User-written Go tools are trusted code and run in the process you compiled
them into; put untrusted work in a sandbox.
