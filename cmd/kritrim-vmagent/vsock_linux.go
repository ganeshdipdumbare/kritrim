//go:build linux

package main

import (
	"os"
	"syscall"
	"unsafe"

	"github.com/ganeshdipdumbare/kritrim/internal/vmagent"
)

const afVsock = 40

// childEnv marks the server process PID 1 starts.
const childEnv = "KRITRIM_VMAGENT_CHILD"

// sockaddrVM is struct sockaddr_vm from <linux/vm_sockets.h>.
type sockaddrVM struct {
	family    uint16
	reserved1 uint16
	port      uint32
	cid       uint32
	flags     uint8
	zero      [3]uint8
}

const vmaddrCIDAny = 0xFFFFFFFF

// serveVsock listens on vsock with raw system calls; the syscall package
// has no AF_VSOCK address type, and connections are used as plain files.
func serveVsock(env []string) error {
	fd, err := syscall.Socket(afVsock, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	sa := sockaddrVM{family: afVsock, port: vmagent.Port, cid: vmaddrCIDAny}
	if _, _, e := syscall.RawSyscall(syscall.SYS_BIND, uintptr(fd), uintptr(unsafe.Pointer(&sa)), unsafe.Sizeof(sa)); e != 0 {
		return e
	}
	if err := syscall.Listen(fd, 64); err != nil {
		return err
	}
	for {
		nfd, _, e := syscall.Syscall6(syscall.SYS_ACCEPT4, uintptr(fd), 0, 0, syscall.SOCK_CLOEXEC, 0, 0)
		if e != 0 {
			if e == syscall.EINTR {
				continue
			}
			return e
		}
		go vmagent.Serve(os.NewFile(nfd, "vsock"), env)
	}
}

// initSystem prepares a minimal userland when the agent is PID 1.
func initSystem() error {
	mounts := []struct {
		src, target, fstype string
		flags               uintptr
		data                string
	}{
		{"proc", "/proc", "proc", syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC, ""},
		{"sysfs", "/sys", "sysfs", syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC, ""},
		{"devtmpfs", "/dev", "devtmpfs", syscall.MS_NOSUID, "mode=0755"},
		{"tmpfs", "/tmp", "tmpfs", syscall.MS_NOSUID | syscall.MS_NODEV, "mode=1777"},
		{"tmpfs", "/run", "tmpfs", syscall.MS_NOSUID | syscall.MS_NODEV, "mode=0755"},
	}
	var first error
	for _, m := range mounts {
		_ = os.MkdirAll(m.target, 0o755)
		if err := syscall.Mount(m.src, m.target, m.fstype, m.flags, m.data); err != nil && err != syscall.EBUSY && first == nil {
			first = err
		}
	}
	return first
}

// superviseAsInit runs the server as a child process and reaps every
// orphan. Reaping in the server itself would steal the exit statuses its
// own commands wait for.
func superviseAsInit() {
	self, err := os.Executable()
	if err != nil {
		self = "/proc/self/exe"
	}
	for {
		p, err := os.StartProcess(self, os.Args, &os.ProcAttr{
			Env:   append(os.Environ(), childEnv+"=1"),
			Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		})
		if err != nil {
			os.Stderr.WriteString("kritrim-vmagent: start server: " + err.Error() + "\n")
			syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
			return
		}
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, 0, nil)
			if err == syscall.EINTR {
				continue
			}
			if err != nil || pid == p.Pid {
				break
			}
		}
	}
}
