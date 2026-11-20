//go:build linux && amd64

package kritrim

const (
	auditArch = 0xc000003e // AUDIT_ARCH_X86_64
	x32Bit    = 0x40000000 // __X32_SYSCALL_BIT

	sysClone  = 56
	sysClone3 = 435
	sysSocket = 41
)

// deniedSyscalls fail with EPERM in a Confined sandbox.
var deniedSyscalls = []uint32{
	101,      // ptrace
	310, 311, // process_vm_readv, process_vm_writev
	438,      // pidfd_getfd
	165, 166, // mount, umount2
	155,                          // pivot_root
	428, 429, 430, 431, 432, 433, // open_tree, move_mount, fsopen, fsconfig, fsmount, fspick
	442,      // mount_setattr
	272, 308, // unshare, setns
	321,           // bpf
	298,           // perf_event_open
	323,           // userfaultfd
	425, 426, 427, // io_uring_setup, io_uring_enter, io_uring_register
	248, 249, 250, // add_key, request_key, keyctl
	303, 304, // name_to_handle_at, open_by_handle_at
	175, 313, 176, // init_module, finit_module, delete_module
	246, 320, // kexec_load, kexec_file_load
	163,      // acct
	167, 168, // swapon, swapoff
	169, // reboot
	103, // syslog
	179, // quotactl
	135, // personality (can disable ASLR)
}
