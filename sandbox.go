package kritrim

import (
	"context"
	"io"
	"io/fs"
	"time"
)

// Sandbox creates an isolated machine for tools or for the whole agent.
// Third-party providers implement this interface; see sandbox/README.md.
type Sandbox interface {
	Open(ctx context.Context, opts OpenConfig) (Machine, error)
}

// OpenConfig describes the machine to create.
type OpenConfig struct {
	Image   string
	Network bool // false by default: no egress
	Env     []string
}

// Machine is one running sandbox.
type Machine interface {
	Exec(ctx context.Context, req ExecRequest) (ExecResult, error)
	Start(ctx context.Context, req StartRequest) (Process, error)
	Read(ctx context.Context, path string) ([]byte, error)
	Write(ctx context.Context, path string, data []byte, mode fs.FileMode) error
	Close(ctx context.Context) error
}

// ExecRequest is one command run inside a machine.
type ExecRequest struct {
	Command []string
	Dir     string
	Env     []string
	Stdin   []byte
	Timeout time.Duration
}

// ExecResult is the outcome of Exec.
type ExecResult struct {
	Stdout string
	Stderr string
	Exit   int
}

// StartRequest starts a long-lived process with stdio, used to run the agent
// inside the box.
type StartRequest struct {
	Command []string
	Dir     string
	Env     []string
}

// Process is a long-lived process inside a machine.
type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Wait() error
	Kill() error
}
