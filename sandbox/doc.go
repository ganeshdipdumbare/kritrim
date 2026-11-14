// Package sandbox is the home of kritrim's sandbox adapters. The contract
// they implement (kritrim.Sandbox, kritrim.Machine, kritrim.Process) lives
// in the root package; each provider has its own subpackage:
//
//	sandbox/ssh          a remote host over SSH
//	sandbox/e2b          E2B cloud sandboxes
//	sandbox/daytona      Daytona workspaces
//	sandbox/microsandbox a self-hosted microsandbox server
//	sandbox/tensorlake   Tensorlake sandboxes
//	sandbox/firecracker  Firecracker microVMs on a Linux/KVM host
//
// Docker and Local ship in the root package. Every adapter must pass the
// conformance suite in sandbox/sandboxtest; see README.md for the rules.
package sandbox
