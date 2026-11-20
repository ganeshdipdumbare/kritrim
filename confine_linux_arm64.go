//go:build linux && arm64

package kritrim

const (
	auditArch = 0xc00000b7 // AUDIT_ARCH_AARCH64
	x32Bit    = 0          // no x32 ABI on arm64

	sysClone  = 220
	sysClone3 = 435
	sysSocket = 198
)

// deniedSyscalls fail with EPERM in a Confined sandbox.
var deniedSyscalls = []uint32{
	117,      // ptrace
	270, 271, // process_vm_readv, process_vm_writev
	438,    // pidfd_getfd
	40, 39, // mount, umount2
	41,                           // pivot_root
	428, 429, 430, 431, 432, 433, // open_tree, move_mount, fsopen, fsconfig, fsmount, fspick
	442,     // mount_setattr
	97, 268, // unshare, setns
	280,           // bpf
	241,           // perf_event_open
	282,           // userfaultfd
	425, 426, 427, // io_uring_setup, io_uring_enter, io_uring_register
	217, 218, 219, // add_key, request_key, keyctl
	264, 265, // name_to_handle_at, open_by_handle_at
	105, 273, 106, // init_module, finit_module, delete_module
	104, 294, // kexec_load, kexec_file_load
	89,       // acct
	224, 225, // swapon, swapoff
	142, // reboot
	116, // syslog
	60,  // quotactl
	92,  // personality (can disable ASLR)
}
