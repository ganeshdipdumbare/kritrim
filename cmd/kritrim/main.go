// Command kritrim is a small developer helper.
//
//	kritrim box build [-o box] [-os linux] [-arch amd64] <package>
//	kritrim vmagent build [-o kritrim-vmagent] [-arch amd64]
//
// box build compiles the box binary for Sealed with the right GOOS/GOARCH,
// so you do not have to remember the cross-compile flags. vmagent build
// compiles the guest agent a Firecracker root filesystem needs.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

const vmagentPkg = "github.com/ganeshdipdumbare/kritrim/cmd/kritrim-vmagent"

func main() {
	if len(os.Args) < 3 || os.Args[2] != "build" {
		usage()
	}
	switch os.Args[1] {
	case "box":
		fs := flag.NewFlagSet("box build", flag.ExitOnError)
		out := fs.String("o", "box", "output binary")
		goos := fs.String("os", "linux", "target GOOS")
		goarch := fs.String("arch", defaultArch(), "target GOARCH")
		fs.Parse(os.Args[3:])
		pkg := "."
		if fs.NArg() > 0 {
			pkg = fs.Arg(0)
		}
		build(*out, *goos, *goarch, pkg)
	case "vmagent":
		fs := flag.NewFlagSet("vmagent build", flag.ExitOnError)
		out := fs.String("o", "kritrim-vmagent", "output binary")
		goarch := fs.String("arch", defaultArch(), "target GOARCH")
		fs.Parse(os.Args[3:])
		build(*out, "linux", *goarch, vmagentPkg)
	default:
		usage()
	}
}

func build(out, goos, goarch, pkg string) {
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, pkg)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "kritrim:", err)
		os.Exit(1)
	}
	fmt.Printf("built %s for %s/%s\n", out, goos, goarch)
}

func defaultArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	default:
		return "amd64"
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:\n  kritrim box build [-o box] [-os linux] [-arch amd64] [package]\n  kritrim vmagent build [-o kritrim-vmagent] [-arch amd64]")
	os.Exit(2)
}
