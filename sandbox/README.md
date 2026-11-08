# Sandbox providers

A sandbox is anything that can run a process, copy files and be destroyed.
Docker, Local and SSH ship in the repository; every adapter must pass the
conformance suite in [`sandboxtest`](sandboxtest/sandboxtest.go). E2B,
microsandbox, Firecracker, Tensorlake, Daytona and cloud VMs plug in by
implementing the same interfaces — no core changes.

```go
import (
	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/sandbox/ssh"
)

box, err := ssh.New(ssh.Config{
	Host:       "ubuntu@10.0.1.5",
	WorkingDir: "/home/ubuntu/workspace",
	IdentityFile: "~/.ssh/agent_key",
})
```


```go
type Sandbox interface {
	Open(ctx context.Context, opts OpenConfig) (Machine, error)
}

type Machine interface {
	Exec(ctx context.Context, req ExecRequest) (ExecResult, error)
	Start(ctx context.Context, req StartRequest) (Process, error)
	Read(ctx context.Context, path string) ([]byte, error)
	Write(ctx context.Context, path string, data []byte, mode fs.FileMode) error
	Close(ctx context.Context) error
}

type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Wait() error
	Kill() error
}
```

`OpenConfig.Network` is false by default. Ask for it only when the provider
cannot enforce isolation otherwise.

## What the core uses

| Mode | Methods |
|---|---|
| Tools in a sandbox (`Config.Sandbox`) | `Exec`, `Read`, `Write`, `Close` |
| Sealed agent (`Config.Sealed`) | `Open`, `Write` (upload binary), `Start` (stdio RPC), `Close` |

`Start` must give the caller the process's real stdin and stdout. For an
HTTP-only provider, that usually means a PTY or a websocket that a small shim
binary exposes.

## Invariants a provider must hold

1. **Destroy on `Close`.** No container, VM, or remote session may outlive it.
2. **No secrets.** The host never passes secrets to the provider. Anything the
   box needs arrives as a placeholder or an RPC.
3. **Network off by default.** If `OpenConfig.Network` is false, the box must
   not reach the network.
4. **Fresh filesystem.** Each `Open` starts empty (or from the image only).
5. **Uploaded binary runs.** `Sealed.Binary` must be executable in the box and
   match its OS/arch (`GOOS=linux` for containers).

## Example skeleton

```go
package mybox

type Sandbox struct{ /* endpoint, token */ }

func (s *Sandbox) Open(ctx context.Context, _ kritrim.OpenConfig) (kritrim.Machine, error) {
	return &machine{id: newID()}, nil
}

type machine struct{ id string }

func (m *machine) Exec(ctx context.Context, r kritrim.ExecRequest) (kritrim.ExecResult, error) { ... }
func (m *machine) Start(ctx context.Context, r kritrim.StartRequest) (kritrim.Process, error)  { ... }
func (m *machine) Read(ctx context.Context, p string) ([]byte, error)                          { ... }
func (m *machine) Write(ctx context.Context, p string, b []byte, mode fs.FileMode) error        { ... }
func (m *machine) Close(ctx context.Context) error                                              { ... }
```

Put the adapter in its own module or under `sandbox/<name>`; it imports
`kritrim` and the core never imports it.
