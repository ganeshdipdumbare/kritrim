package ssh

import (
	"os/exec"
	"strings"
	"testing"
)

// An env entry is data, not shell. Run the command the way the remote shell
// would and make sure the entry cannot start a second command.
func TestCommandQuotesEnvEntries(t *testing.T) {
	m := &machine{cfg: Config{WorkingDir: "/"}}
	script, err := m.command([]string{"true"}, "", []string{"FOO=a; echo INJECTED"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("sh: %v: %s", err, out)
	}
	if strings.Contains(string(out), "INJECTED") {
		t.Fatalf("env value ran as shell: %q", out)
	}
}

// A well-formed entry still reaches the command, spaces and all.
func TestCommandKeepsValidEnv(t *testing.T) {
	m := &machine{cfg: Config{WorkingDir: "/"}}
	script, err := m.command([]string{"sh", "-c", `printf %s "$GREETING"`}, "", []string{"GREETING=hello world"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("sh: %v: %s", err, out)
	}
	if string(out) != "hello world" {
		t.Fatalf("got %q", out)
	}
}

// Anything that is not NAME=value is refused, not passed to the shell.
func TestCommandRefusesMalformedEnv(t *testing.T) {
	m := &machine{cfg: Config{WorkingDir: "/"}}
	for _, bad := range []string{"no-equals-sign", "BAD NAME=x", "X;rm=y", "=empty"} {
		if _, err := m.command([]string{"true"}, "", []string{bad}); err == nil {
			t.Fatalf("accepted env entry %q", bad)
		}
	}
}

// Options that load or run local code are refused; ordinary ones pass.
func TestCheckOptionRefusesLocalCodeOptions(t *testing.T) {
	for _, bad := range []string{"PKCS11Provider=/tmp/evil.so", "SecurityKeyProvider /tmp/evil.so",
		"Match exec id", "Include /tmp/x", "ProxyCommand=sh -c id", "LocalCommand=id"} {
		if checkOption(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, ok := range []string{"ServerAliveInterval=5", "IdentitiesOnly=yes", "StrictHostKeyChecking=yes"} {
		if err := checkOption(ok); err != nil {
			t.Errorf("refused %q: %v", ok, err)
		}
	}
}

// A host or identity file starting with "-" would be read by ssh as an option.
func TestNewRefusesOptionLikeHost(t *testing.T) {
	if _, err := New(Config{Host: "-oProxyCommand=id", WorkingDir: "/"}); err == nil {
		t.Fatal("option-like Host accepted")
	}
	if _, err := New(Config{Host: "box", IdentityFile: "-oX=1", WorkingDir: "/"}); err == nil {
		t.Fatal("option-like IdentityFile accepted")
	}
}
