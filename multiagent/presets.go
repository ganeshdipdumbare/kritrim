package multiagent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ganeshdipdumbare/kritrim"
)

// AgentNode runs an agent as a graph node. opts apply to every run, for
// example kritrim.Thread to keep the node's conversation.
func AgentNode(a *kritrim.Agent, opts ...kritrim.Option) Node {
	return func(ctx context.Context, prompt string, _ *State) (*kritrim.Result, error) {
		return a.Run(ctx, prompt, opts...)
	}
}

// Step is one named node of a Sequence.
type Step struct {
	Name string
	Node Node
}

// Sequence builds a graph that runs the steps in order, each one getting
// the previous step's text as its prompt: draft, then review, then polish.
// It is an ordinary Graph, so it can be extended with more edges, capped
// with MaxSteps, run, or handed to an agent with AsTool.
func Sequence(steps ...Step) (*Graph, error) {
	if len(steps) == 0 {
		return nil, errors.New("multiagent: Sequence needs at least one step")
	}
	g := NewGraph()
	seen := map[string]bool{}
	for i, s := range steps {
		if s.Name == "" || s.Node == nil {
			return nil, fmt.Errorf("multiagent: step %d needs a name and a node", i)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("multiagent: duplicate step %q", s.Name)
		}
		seen[s.Name] = true
		g.Node(s.Name, s.Node)
		if i > 0 {
			g.Edge(steps[i-1].Name, s.Name, nil)
		}
	}
	return g.Start(steps[0].Name).End(steps[len(steps)-1].Name), nil
}

// RouteKey is the State key where Router records the chosen route.
const RouteKey = "route"

// Router builds a graph that sends the prompt to exactly one route.
// classify names the route (a model call, a keyword match, a lookup); the
// chosen route gets the original prompt, and its result is the graph's
// result. A name that is not a route fails the run instead of guessing.
//
//	g, _ := multiagent.Router(classify, map[string]multiagent.Node{
//		"billing": multiagent.AgentNode(billing),
//		"tech":    multiagent.AgentNode(tech),
//	})
func Router(classify func(ctx context.Context, prompt string) (string, error), routes map[string]Node) (*Graph, error) {
	if classify == nil || len(routes) == 0 {
		return nil, errors.New("multiagent: Router needs a classifier and at least one route")
	}
	names := make([]string, 0, len(routes))
	for name, n := range routes {
		if name == "" || name == "classify" || n == nil {
			return nil, fmt.Errorf("multiagent: invalid route %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	g := NewGraph()
	g.Node("classify", func(ctx context.Context, prompt string, st *State) (*kritrim.Result, error) {
		route, err := classify(ctx, prompt)
		if err != nil {
			return nil, err
		}
		route = strings.TrimSpace(route)
		if routes[route] == nil {
			return nil, fmt.Errorf("multiagent: classifier chose %q, not one of %s", route, strings.Join(names, ", "))
		}
		st.Set(RouteKey, route)
		return &kritrim.Result{Text: prompt, Stop: kritrim.StopDone}, nil
	})
	for _, name := range names {
		route := name
		g.Node(route, routes[route])
		g.Edge("classify", route, func(st *State) bool { return st.Get(RouteKey) == route })
		g.End(route)
	}
	return g.Start("classify"), nil
}
