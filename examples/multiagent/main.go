// Compose agents as a graph and as a swarm.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/multiagent"
)

func main() {
	ctx := context.Background()
	triage, _ := kritrim.New(kritrim.Config{Model: kritrim.Mock(
		kritrim.TurnTool("handoff_to_writer", map[string]any{"task": "write it"}),
		kritrim.Turn{Text: "handed off"},
	)})
	writer, _ := kritrim.New(kritrim.Config{Model: kritrim.Mock(kritrim.Turn{Text: "draft ready"})})
	defer triage.Close()
	defer writer.Close()

	swarm, err := multiagent.NewSwarm(
		multiagent.Member{Name: "triage", Description: "routes work", Agent: triage},
		multiagent.Member{Name: "writer", Description: "writes", Agent: writer},
	)
	if err != nil {
		log.Fatal(err)
	}
	res, err := swarm.Run(ctx, "draft the launch post")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("swarm:", res.Text)

	graph := multiagent.NewGraph().Start("fetch").
		Node("fetch", func(ctx context.Context, prompt string, s *multiagent.State) (*kritrim.Result, error) {
			s.Set("data", "42")
			return &kritrim.Result{Text: "fetched", Stop: "stop"}, nil
		}).
		Node("report", func(ctx context.Context, prompt string, s *multiagent.State) (*kritrim.Result, error) {
			return &kritrim.Result{Text: "value=" + fmt.Sprint(s.Get("data")), Stop: "stop"}, nil
		}).
		Edge("fetch", "report", nil)
	res, err = graph.Run(ctx, "start")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("graph:", res.Text)
}
