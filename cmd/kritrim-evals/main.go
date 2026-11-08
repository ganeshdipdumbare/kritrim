// Command kritrim-evals renders and diagnoses evaluation reports.
//
//	kritrim-evals report report.json
//	kritrim-evals diagnose report.json
//
// Experiments are Go code (see package evals); this CLI works on the JSON a
// report writes, so it fits any CI runner.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ganeshdipdumbare/kritrim/evals"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "write JSON")
	fs.Parse(os.Args[2:])
	if fs.NArg() != 1 {
		usage()
		os.Exit(2)
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "kritrim-evals:", err)
		os.Exit(2)
	}
	defer f.Close()
	rep, err := evals.ReadReport(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kritrim-evals:", err)
		os.Exit(2)
	}
	switch cmd {
	case "report":
		if *jsonOut {
			_ = rep.WriteJSON(os.Stdout)
		} else {
			fmt.Print(rep.Summary())
		}
		if !rep.OK() {
			os.Exit(1)
		}
	case "diagnose":
		for _, r := range rep.Results {
			if len(r.Findings) == 0 {
				continue
			}
			fmt.Printf("%s\n", r.Case.Name)
			for _, f := range r.Findings {
				fmt.Printf("  %s: %s (step %d)\n", f.Kind, f.Detail, f.Step)
			}
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: kritrim-evals <report|diagnose> [--json] report.json")
}
