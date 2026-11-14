package multiagent

import (
	"context"
	"fmt"

	"github.com/ganeshdipdumbare/kritrim"
)

// AsTool exposes the graph as a tool an agent can call, the same way
// Agent.AsTool exposes one agent to another.
//
//	router := multiagent.NewGraph().Start("triage"). /* ... */
//	front, _ := kritrim.New(kritrim.Config{
//		Model: model,
//		Tools: []kritrim.Tool{router.AsTool("route_ticket", "Routes a support ticket and answers it")},
//	})
//
// Write the description for the caller, not the graph: the model picks the
// tool from that sentence alone and never sees the nodes.
//
// The graph runs under the caller's run, so a cancelled or timed-out caller
// stops the graph too. A node that pauses for an approval cannot be
// resumed from inside a tool call, so the tool reports that a human is
// needed and the calling model relays it; drive such a graph with Run
// instead.
func (g *Graph) AsTool(name, description string) kritrim.Tool {
	return asTool(name, description, g.Run)
}

// AsTool exposes the swarm as a tool an agent can call. The caveats on
// Graph.AsTool apply here too.
func (s *Swarm) AsTool(name, description string) kritrim.Tool {
	return asTool(name, description, func(ctx context.Context, prompt string) (*kritrim.Result, error) {
		return s.Run(ctx, prompt)
	})
}

func asTool(name, description string, run func(context.Context, string) (*kritrim.Result, error)) kritrim.Tool {
	return kritrim.NewTool(name, description, func(ctx context.Context, in struct {
		Task string `json:"task" desc:"the task, with all the context it needs"`
	}) (string, error) {
		res, err := run(ctx, in.Task)
		if err != nil {
			return "", err
		}
		if res.Stop == kritrim.StopInterrupt {
			return "", fmt.Errorf("%s needs an approval it cannot ask for here; tell the user", name)
		}
		return res.Text, nil
	})
}
