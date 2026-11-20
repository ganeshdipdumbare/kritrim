package kritrim

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ConfinedConfig describes a Confined sandbox.
type ConfinedConfig struct {
	// ReadOnly lists extra paths commands may read and execute. The system
	// directories (/usr, /lib, /lib64, /bin, /sbin, /etc) and a few devices
	// are always readable. A listed path that does not exist is an error.
	ReadOnly []string
	// ReadWrite lists extra paths commands may read, write and execute. A
	// private workspace directory is always read-write; it is the default
	// working directory and TMPDIR, and Close removes it.
	ReadWrite []string
	// Network lets commands open TCP and UDP sockets. Default false: only
	// Unix sockets can be created and no TCP connection or bind is allowed.
	Network bool
}

// Confined returns a sandbox that runs commands on this host under kernel
// confinement, in the spirit of OpenShell's process sandbox:
//
//   - Landlock limits the filesystem to the system directories (read-only)
//     and the paths the config lists, and, without Network, forbids TCP
//     connect and bind;
//   - a seccomp filter denies ptrace, process_vm_*, mount and the new mount
//     API, namespaces (unshare, setns, clone with namespace flags), bpf,
//     perf_event_open, userfaultfd, io_uring, kernel keyrings, module and
//     kexec loading, and, without Network, every socket family except Unix;
//   - no_new_privs is set, core dumps are off, and the process environment
//     holds none of the host's secrets.
//
// It needs Linux 6.7 or newer (Landlock ABI 4) on amd64 or arm64; Open
// fails rather than run unconfined anywhere else. On Linux 6.12 or newer
// commands also cannot signal processes or reach abstract Unix sockets
// outside the sandbox.
//
// Commands run as the host user. The process that calls Run must be built
// with this package: commands start by re-executing it, and its init
// applies the confinement before the command is executed.
func Confined(cfg ConfinedConfig) Sandbox { return &confinedSandbox{cfg: cfg} }

type confinedSandbox struct{ cfg ConfinedConfig }

// confineSpec is what the re-executed process reads to confine itself.
type confineSpec struct {
	Path      string   `json:"path"`
	Args      []string `json:"args"`
	ReadOnly  []string `json:"ro"`
	ReadWrite []string `json:"rw"`
	Network   bool     `json:"net"`
	Env       []string `json:"env"`
}

// confineEnv carries a confineSpec to the re-executed process.
const confineEnv = "KRITRIM_CONFINE"

// defaultReadOnly are readable in every Confined sandbox. Missing ones are
// skipped. /proc is left out on purpose: same-user processes' environ and
// memory maps are readable there.
var defaultReadOnly = []string{"/usr", "/lib", "/lib64", "/lib32", "/bin", "/sbin", "/etc",
	"/dev/urandom", "/dev/random"}

// defaultReadWrite are writable devices every program expects.
var defaultReadWrite = []string{"/dev/null", "/dev/zero", "/dev/full"}

func (s *confinedSandbox) Open(ctx context.Context, opts OpenConfig) (Machine, error) {
	if opts.Image != "" {
		return nil, errors.New("kritrim: Confined ignores Image")
	}
	network := s.cfg.Network || opts.Network
	if err := confineSupported(network); err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("kritrim: Confined needs its own executable: %w", err)
	}
	var ro, rw []string
	for _, p := range s.cfg.ReadOnly {
		a, err := confinePath(p)
		if err != nil {
			return nil, err
		}
		ro = append(ro, a)
	}
	for _, p := range s.cfg.ReadWrite {
		a, err := confinePath(p)
		if err != nil {
			return nil, err
		}
		rw = append(rw, a)
	}
	ws, err := os.MkdirTemp("", "kritrim-confined-")
	if err != nil {
		return nil, err
	}
	ws, err = filepath.EvalSymlinks(ws)
	if err != nil {
		return nil, err
	}
	return &localMachine{confine: &confineSpec{ReadOnly: ro, ReadWrite: append(rw, ws), Network: network},
		self: self, workspace: ws}, nil
}

// confinePath checks a configured path: absolute, no "..", not "/", and
// present. It returns the path with symlinks resolved, since Landlock rules
// attach to the inode the path names when the rule is made.
func confinePath(p string) (string, error) {
	if !filepath.IsAbs(p) || strings.Contains(p, "..") {
		return "", fmt.Errorf("kritrim: Confined path %q must be absolute and contain no parent-directory segments", p)
	}
	a, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", fmt.Errorf("kritrim: Confined path %q: %w", p, err)
	}
	if a == "/" {
		return "", errors.New("kritrim: Confined may not grant the whole filesystem")
	}
	return a, nil
}

// confinedCmd builds the command that re-executes this program, confines
// itself and then executes argv.
func (m *localMachine) confinedCmd(ctx context.Context, argv []string, dir string, env []string) (*exec.Cmd, error) {
	path, err := lookPathIn(argv[0], env)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		dir = m.workspace
	}
	// The re-executed process gets only the spec: the command's own
	// environment (which could hold LD_PRELOAD and the like) applies from
	// the final exec on, after the confinement is in place.
	child := childEnv(env)
	// TMPDIR is the workspace: the shared /tmp is not writable.
	child = append(withoutEnv(child, "TMPDIR", "TMP", "TEMP", confineEnv), "TMPDIR="+m.workspace)
	spec := *m.confine
	spec.Path, spec.Args, spec.Env = path, argv, child
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	if ctx != nil {
		cmd = exec.CommandContext(ctx, m.self)
	} else {
		cmd = exec.Command(m.self)
	}
	cmd.Args = []string{argv[0]}
	cmd.Dir = dir
	cmd.Env = []string{confineEnv + "=" + base64.StdEncoding.EncodeToString(raw)}
	return cmd, nil
}

// lookPathIn resolves name like a shell would with the child's PATH.
func lookPathIn(name string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		return filepath.Abs(name)
	}
	pathVar := os.Getenv("PATH")
	for _, e := range childEnv(env) {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			pathVar = v
		}
	}
	for _, dir := range filepath.SplitList(pathVar) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("kritrim: %q not found in PATH", name)
}

func withoutEnv(env []string, names ...string) []string {
	out := env[:0:0]
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		drop := false
		for _, n := range names {
			if k == n {
				drop = true
			}
		}
		if !drop {
			out = append(out, e)
		}
	}
	return out
}
