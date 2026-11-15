//go:build unix

package main

import (
	"net"
	"syscall"
)

// listenUnixPrivate creates the socket owner-only. The umask is set around the
// listen, so the socket never exists with wider permissions.
func listenUnixPrivate(path string) (net.Listener, error) {
	old := syscall.Umask(0o077)
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	// The umask sets the mode at creation. A chmod by path would follow a
	// symlink planted at that path in the meantime, so none is made.
	return l, nil
}
