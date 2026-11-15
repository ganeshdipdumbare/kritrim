//go:build unix

package main

import (
	"net"
	"os"
	"syscall"
)

// listenUnixPrivate creates the socket owner-only. The umask is set around the
// listen, so the socket never exists with wider permissions, and the chmod
// covers any platform that ignores the umask for sockets.
func listenUnixPrivate(path string) (net.Listener, error) {
	old := syscall.Umask(0o077)
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}
