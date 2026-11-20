//go:build linux && (amd64 || arm64)

package kritrim

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// The re-executed process confines itself here, before main runs, and
// replaces itself with the command. It never returns.
func init() {
	raw, ok := os.LookupEnv(confineEnv)
	if !ok {
		return
	}
	// Landlock and seccomp apply to the calling thread and survive execve,
	// which keeps only that thread: lock it so all three run on one.
	runtime.LockOSThread()
	err := confineAndExec(raw)
	fmt.Fprintf(os.Stderr, "kritrim: confined exec failed: %v\n", err)
	os.Exit(126)
}

func confineAndExec(raw string) error {
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return err
	}
	var s confineSpec
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s.Path == "" || len(s.Args) == 0 {
		return errors.New("empty command")
	}
	abi, err := landlockABI()
	if err != nil {
		return err
	}
	if err := confineRequire(abi, s.Network); err != nil {
		return err
	}
	// No core dumps and no ptrace attach by same-user processes.
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{}); err != nil {
		return fmt.Errorf("rlimit core: %w", err)
	}
	if err := prctl(prPRSetDumpable, 0); err != nil {
		return fmt.Errorf("dumpable: %w", err)
	}
	if err := prctl(prSetNoNewPrivs, 1); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if err := landlockApply(abi, s); err != nil {
		return fmt.Errorf("landlock: %w", err)
	}
	if err := seccompApply(s.Network); err != nil {
		return fmt.Errorf("seccomp: %w", err)
	}
	return syscall.Exec(s.Path, s.Args, s.Env)
}

// confineSupported reports, in the parent, whether this kernel can enforce
// a Confined sandbox. Open fails closed when it cannot.
func confineSupported(network bool) error {
	abi, err := landlockABI()
	if err != nil {
		return fmt.Errorf("kritrim: Confined needs Landlock: %w", err)
	}
	return confineRequire(abi, network)
}

func confineRequire(abi int, network bool) error {
	switch {
	case abi < 3:
		return fmt.Errorf("kritrim: Confined needs Landlock ABI 3 (Linux 6.2+), kernel has %d", abi)
	case !network && abi < 4:
		return fmt.Errorf("kritrim: Confined without Network needs Landlock ABI 4 (Linux 6.7+), kernel has %d", abi)
	}
	return nil
}

const (
	prSetNoNewPrivs = 38
	prPRSetDumpable = 4
	prSetSeccomp    = 22
	seccompModeFilt = 2

	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446

	landlockCreateRulesetVersion = 1
	landlockRulePathBeneath      = 1
)

// Landlock filesystem rights.
const (
	llExecute    = 1 << 0
	llWriteFile  = 1 << 1
	llReadFile   = 1 << 2
	llReadDir    = 1 << 3
	llRemoveDir  = 1 << 4
	llRemoveFile = 1 << 5
	llMakeChar   = 1 << 6
	llMakeDir    = 1 << 7
	llMakeReg    = 1 << 8
	llMakeSock   = 1 << 9
	llMakeFifo   = 1 << 10
	llMakeBlock  = 1 << 11
	llMakeSym    = 1 << 12
	llRefer      = 1 << 13 // ABI 2
	llTruncate   = 1 << 14 // ABI 3
	llIoctlDev   = 1 << 15 // ABI 5

	llNetBindTCP    = 1 << 0 // ABI 4
	llNetConnectTCP = 1 << 1

	llScopeAbstractUnix = 1 << 0 // ABI 6
	llScopeSignal       = 1 << 1

	llFileRights = llExecute | llWriteFile | llReadFile | llTruncate | llIoctlDev
)

func prctl(op, arg uintptr) error {
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, op, arg, 0, 0, 0, 0); e != 0 {
		return e
	}
	return nil
}

func landlockABI() (int, error) {
	v, _, e := syscall.RawSyscall(sysLandlockCreateRuleset, 0, 0, landlockCreateRulesetVersion)
	if e != 0 {
		return 0, fmt.Errorf("landlock unavailable: %w", e)
	}
	return int(v), nil
}

// handledFS is every filesystem right this ABI knows: whatever is handled
// and not granted by a rule is denied.
func handledFS(abi int) uint64 {
	h := uint64(llExecute | llWriteFile | llReadFile | llReadDir | llRemoveDir | llRemoveFile |
		llMakeChar | llMakeDir | llMakeReg | llMakeSock | llMakeFifo | llMakeBlock | llMakeSym)
	if abi >= 2 {
		h |= llRefer
	}
	if abi >= 3 {
		h |= llTruncate
	}
	if abi >= 5 {
		h |= llIoctlDev
	}
	return h
}

func landlockApply(abi int, s confineSpec) error {
	fsAll := handledFS(abi)
	// ruleset_attr grows with the ABI; pass the size this ABI knows.
	attr := make([]byte, 24)
	binary.LittleEndian.PutUint64(attr[0:], fsAll)
	size := 8
	if abi >= 4 {
		size = 16
		if !s.Network {
			binary.LittleEndian.PutUint64(attr[8:], llNetBindTCP|llNetConnectTCP)
		}
	}
	if abi >= 6 {
		size = 24
		binary.LittleEndian.PutUint64(attr[16:], llScopeAbstractUnix|llScopeSignal)
	}
	fd, _, e := syscall.RawSyscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr[0])), uintptr(size), 0)
	if e != 0 {
		return fmt.Errorf("create ruleset: %w", e)
	}
	defer syscall.Close(int(fd))

	read := uint64(llExecute | llReadFile | llReadDir)
	write := fsAll // everything handled, devices excepted below
	write &^= llMakeChar | llMakeBlock | llIoctlDev
	add := func(path string, access uint64, required bool) error {
		pfd, err := syscall.Open(path, oPath|syscall.O_CLOEXEC, 0)
		if err != nil {
			if !required && errors.Is(err, syscall.ENOENT) {
				return nil
			}
			return fmt.Errorf("%s: %w", path, err)
		}
		defer syscall.Close(pfd)
		var st syscall.Stat_t
		if err := syscall.Fstat(pfd, &st); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
			access &= llFileRights
		}
		access &= fsAll
		// struct landlock_path_beneath_attr is packed: u64 then s32.
		var rule [12]byte
		binary.LittleEndian.PutUint64(rule[0:], access)
		binary.LittleEndian.PutUint32(rule[8:], uint32(pfd))
		if _, _, e := syscall.RawSyscall6(sysLandlockAddRule, fd, landlockRulePathBeneath,
			uintptr(unsafe.Pointer(&rule[0])), 0, 0, 0); e != 0 {
			return fmt.Errorf("add rule %s: %w", path, e)
		}
		return nil
	}
	for _, p := range defaultReadOnly {
		if err := add(p, read, false); err != nil {
			return err
		}
	}
	for _, p := range defaultReadWrite {
		if err := add(p, llReadFile|llWriteFile|llTruncate, false); err != nil {
			return err
		}
	}
	for _, p := range s.ReadOnly {
		if err := add(p, read, true); err != nil {
			return err
		}
	}
	for _, p := range s.ReadWrite {
		if err := add(p, write, true); err != nil {
			return err
		}
	}
	// The command itself is always readable and executable.
	if err := add(s.Path, llExecute|llReadFile, true); err != nil {
		return err
	}
	if _, _, e := syscall.RawSyscall(sysLandlockRestrictSelf, fd, 0, 0); e != 0 {
		return fmt.Errorf("restrict self: %w", e)
	}
	return nil
}

// Seccomp BPF.
const (
	bpfLD   = 0x00
	bpfW    = 0x00
	bpfABS  = 0x20
	bpfJMP  = 0x05
	bpfJEQ  = 0x10
	bpfJGE  = 0x30
	bpfJSET = 0x40
	bpfK    = 0x00
	bpfRET  = 0x06

	seccompRetKill  = 0x80000000 // SECCOMP_RET_KILL_PROCESS
	seccompRetErrno = 0x00050000
	seccompRetAllow = 0x7fff0000

	// CLONE_NEW* flags: NEWTIME, NEWNS, NEWCGROUP, NEWUTS, NEWIPC, NEWUSER,
	// NEWPID, NEWNET.
	cloneNSFlags = 0x00000080 | 0x00020000 | 0x02000000 | 0x04000000 | 0x08000000 |
		0x10000000 | 0x20000000 | 0x40000000

	afUnix = 1

	oPath = 0x200000 // O_PATH, the same on amd64 and arm64
)

type sockFilter struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

type sockFprog struct {
	Len    uint16
	Filter *sockFilter
}

func stmt(code uint16, k uint32) sockFilter { return sockFilter{Code: code, K: k} }
func jump(code uint16, k uint32, jt, jf uint8) sockFilter {
	return sockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

// seccompProgram builds the filter. Layout:
//
//	arch check -> kill on a foreign ABI (x32 included)
//	denied syscalls -> EPERM
//	clone3 -> ENOSYS, so libc falls back to clone, whose flags we can see
//	clone with namespace flags -> EPERM
//	socket with a non-Unix family -> EPERM (without Network)
//	everything else -> allow
func seccompProgram(network bool) []sockFilter {
	const (
		offNr   = 0
		offArch = 4
		offArg0 = 16 // low 32 bits of args[0] on little-endian
	)
	eperm := seccompRetErrno | uint32(syscall.EPERM)
	enosys := seccompRetErrno | uint32(syscall.ENOSYS)
	p := []sockFilter{
		stmt(bpfLD|bpfW|bpfABS, offArch),
		jump(bpfJMP|bpfJEQ|bpfK, auditArch, 1, 0),
		stmt(bpfRET|bpfK, seccompRetKill),
		stmt(bpfLD|bpfW|bpfABS, offNr),
	}
	if x32Bit != 0 {
		p = append(p, jump(bpfJMP|bpfJGE|bpfK, x32Bit, 0, 1), stmt(bpfRET|bpfK, seccompRetKill))
	}
	for _, nr := range deniedSyscalls {
		p = append(p, jump(bpfJMP|bpfJEQ|bpfK, nr, 0, 1), stmt(bpfRET|bpfK, eperm))
	}
	p = append(p, jump(bpfJMP|bpfJEQ|bpfK, sysClone3, 0, 1), stmt(bpfRET|bpfK, enosys))
	// clone: test the flags argument, then reload nr for the next check.
	p = append(p,
		jump(bpfJMP|bpfJEQ|bpfK, sysClone, 0, 4),
		stmt(bpfLD|bpfW|bpfABS, offArg0),
		jump(bpfJMP|bpfJSET|bpfK, cloneNSFlags, 0, 1),
		stmt(bpfRET|bpfK, eperm),
		stmt(bpfRET|bpfK, seccompRetAllow),
	)
	if !network {
		p = append(p,
			jump(bpfJMP|bpfJEQ|bpfK, sysSocket, 0, 4),
			stmt(bpfLD|bpfW|bpfABS, offArg0),
			jump(bpfJMP|bpfJEQ|bpfK, afUnix, 1, 0),
			stmt(bpfRET|bpfK, eperm),
			stmt(bpfRET|bpfK, seccompRetAllow),
		)
	}
	return append(p, stmt(bpfRET|bpfK, seccompRetAllow))
}

func seccompApply(network bool) error {
	prog := seccompProgram(network)
	fp := sockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prSetSeccomp, seccompModeFilt, uintptr(unsafe.Pointer(&fp))); e != 0 {
		return e
	}
	runtime.KeepAlive(prog)
	return nil
}
