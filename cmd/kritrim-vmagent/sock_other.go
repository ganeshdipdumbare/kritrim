//go:build !unix

package main

import (
	"errors"
	"net"
)

// listenUnixPrivate fails closed: a socket here cannot be made owner-only, and
// the agent serves commands without authenticating the peer.
func listenUnixPrivate(path string) (net.Listener, error) {
	return nil, errors.New("private unix sockets are not supported on this platform; use vsock")
}
