// Command boxtest is a sealed-box program used by the integration test. It is
// built for the host OS and run by Sealed with Local.
package main

import (
	"context"
	"log"
	"os"

	"github.com/ganeshdipdumbare/kritrim"
)

func main() {
	refund := kritrim.Risky(kritrim.NewTool("refund", "refund an order", func(ctx context.Context, in struct {
		Order string `json:"order"`
	}) (string, error) {
		return "refunded " + in.Order, nil
	}))
	cfg := kritrim.Config{
		Model: kritrim.Mock(
			kritrim.TurnTool("refund", map[string]any{"order": "A-17"}),
			kritrim.Turn{Text: "done"},
		),
		Tools: []kritrim.Tool{refund},
	}
	if kritrim.InBox() {
		if err := kritrim.ServeBox(context.Background(), os.Stdin, os.Stdout, cfg); err != nil {
			log.Fatal(err)
		}
		return
	}
	log.Fatal("boxtest is only run as a sealed box")
}
