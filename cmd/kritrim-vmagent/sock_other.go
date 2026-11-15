//go:build !unix

package main

import "net"

// listenUnixPrivate is plain Listen where unix sockets are not private by
// default. Those hosts do not run this guest agent.
func listenUnixPrivate(path string) (net.Listener, error) {
	return net.Listen("unix", path)
}
