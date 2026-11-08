// The whole agent running inside a sandbox. The host is the only way out:
// model calls, secrets, approvals and audit all cross the RPC boundary.
//
// This example uses the local sandbox so it runs anywhere. The local box is
// not an isolation boundary; use Docker (or a cloud sandbox) in production.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/ganeshdipdumbare/kritrim"
)

func main() {
	cfg := kritrim.Config{
		Model: kritrim.Mock(
			kritrim.TurnTool("current_time", nil),
			kritrim.Turn{Text: "I checked the clock inside the box."},
		),
		Tools: []kritrim.Tool{kritrim.Time()},
		Sealed: &kritrim.Sealed{
			Sandbox:          kritrim.Local(),
			AllowUnsafeLocal: true,
			MaxTokens:        10_000,
		},
	}

	// The box runs this same program. The host starts it with KRITRIM_BOX=1.
	if kritrim.InBox() {
		if err := kritrim.ServeBox(context.Background(), os.Stdin, os.Stdout, cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	a, err := kritrim.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer a.Close()

	res, err := a.Run(context.Background(), "What time is it?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Text)
}
