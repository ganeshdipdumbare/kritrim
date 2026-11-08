// A typed tool, a scripted model that calls it, and streaming events.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ganeshdipdumbare/kritrim"
)

func main() {
	weather := kritrim.NewTool("weather", "Get the weather for a city", func(ctx context.Context, in struct {
		City string `json:"city" desc:"city name"`
	}) (string, error) {
		return "18C and light rain in " + in.City, nil
	})

	a, err := kritrim.New(kritrim.Config{
		Model: kritrim.Mock(
			kritrim.TurnTool("weather", map[string]any{"city": "Lisbon"}),
			kritrim.Turn{Text: "It is 18C and raining in Lisbon."},
		),
		Tools: []kritrim.Tool{weather},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer a.Close()

	for e, err := range a.Stream(context.Background(), "What is the weather in Lisbon?") {
		if err != nil {
			log.Fatal(err)
		}
		switch e.Type {
		case "tool_call":
			fmt.Println("calling", e.ToolCall.Name, e.ToolCall.Args)
		case "tool_result":
			fmt.Println("result:", e.ToolResult.Content)
		case "text":
			fmt.Print(e.Text)
		}
	}
	fmt.Println()
}
