//go:build !linux

package main

import "errors"

func serveVsock(env []string) error {
	return errors.New("vsock needs Linux; use -listen unix:/path")
}

func initSystem() error { return errors.New("not supported on this OS") }

func superviseAsInit() {}
