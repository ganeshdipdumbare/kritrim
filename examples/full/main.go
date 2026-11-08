// Sessions, structured output, approvals and a hash-chained audit log.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/ganeshdipdumbare/kritrim"
)

type trip struct {
	City    string   `json:"city"`
	Packing []string `json:"packing" desc:"things to pack"`
}

func main() {
	dir, _ := os.MkdirTemp("", "kritrim")
	defer os.RemoveAll(dir)

	refund := kritrim.Risky(kritrim.NewTool("refund", "Refund an order", func(ctx context.Context, in struct {
		Order string `json:"order"`
	}) (string, error) {
		return "refunded " + in.Order, nil
	}))

	a, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(
			kritrim.Turn{Text: `{"city":"Lisbon","packing":["rain jacket"]}`},
			kritrim.TurnTool("refund", map[string]any{"order": "A-17"}),
			kritrim.Turn{Text: "Refunded A-17."},
			kritrim.Turn{Text: "I refunded A-17 for you."},
		),
		Tools:   []kritrim.Tool{refund},
		Session: kritrim.FileSessions(dir + "/sessions"),
		Audit:   kritrim.AuditFile(dir + "/audit.jsonl"),
		Approve: func(ctx context.Context, req kritrim.Approval) (kritrim.Decision, error) {
			fmt.Println("approve?", req.Message)
			return kritrim.Approve(), nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer a.Close()

	ctx := context.Background()

	var t trip
	if _, err := a.Run(ctx, "Plan a trip to Lisbon", kritrim.Output(&t)); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("structured: %+v\n", t)

	if _, err := a.Run(ctx, "Refund A-17", kritrim.Thread("ticket-981")); err != nil {
		log.Fatal(err)
	}
	res, err := a.Run(ctx, "What did you just do?", kritrim.Thread("ticket-981"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("session:", res.Text)

	if err := kritrim.VerifyAudit(dir + "/audit.jsonl"); err != nil {
		log.Fatal(err)
	}
	fmt.Println("audit chain: ok")
}
