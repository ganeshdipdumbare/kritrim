// Command kritrim-vmagent runs inside a Firecracker microVM and serves the
// kritrim Firecracker sandbox: commands, files and long-lived processes,
// over vsock port 52. Build it for the guest and put it in the root
// filesystem:
//
//	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o kritrim-vmagent ./cmd/kritrim-vmagent
//	sudo mount rootfs.ext4 /mnt && sudo cp kritrim-vmagent /usr/local/bin/ && sudo umount /mnt
//
// The sandbox boots the kernel with init=/usr/local/bin/kritrim-vmagent;
// as PID 1 the agent mounts /proc, /sys, /dev and a /tmp tmpfs before it
// listens. -listen unix:/path serves on a Unix socket instead, for tests.
package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/ganeshdipdumbare/kritrim/internal/vmagent"
)

func main() {
	listen := flag.String("listen", "vsock", `"vsock" or "unix:/path"`)
	flag.Parse()
	if os.Getpid() == 1 {
		if err := initSystem(); err != nil {
			fmt.Fprintln(os.Stderr, "kritrim-vmagent: init:", err)
		}
		superviseAsInit()
		return
	}
	env := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C.UTF-8", "TMPDIR=/tmp"}
	err := serve(*listen, env)
	fmt.Fprintln(os.Stderr, "kritrim-vmagent:", err)
	os.Exit(1)
}

// serve returns only on failure.
func serve(listen string, env []string) error {
	if path, ok := strings.CutPrefix(listen, "unix:"); ok {
		// Only a stale socket is replaced. A live one, or any other file, is
		// refused rather than unlinked.
		if fi, err := os.Lstat(path); err == nil {
			if fi.Mode()&os.ModeSocket == 0 {
				return fmt.Errorf("%s exists and is not a socket", path)
			}
			if c, err := net.Dial("unix", path); err == nil {
				_ = c.Close()
				return fmt.Errorf("%s is in use by another process", path)
			}
			// Remove only the socket that was checked. A different file that
			// appeared since then is left alone.
			if cur, err := os.Lstat(path); err != nil || !os.SameFile(cur, fi) {
				return fmt.Errorf("%s changed while it was being checked", path)
			}
			_ = os.Remove(path)
		}
		// The protocol runs commands and reads and writes files without
		// authenticating the peer, so only the owner may connect.
		l, err := listenUnixPrivate(path)
		if err != nil {
			return err
		}
		for {
			c, err := l.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return err
				}
				// A transient accept failure must not take down the sessions
				// that are running.
				time.Sleep(50 * time.Millisecond)
				continue
			}
			go vmagent.Serve(c, env)
		}
	}
	if listen != "vsock" {
		return fmt.Errorf("unknown -listen %q", listen)
	}
	return serveVsock(env)
}
