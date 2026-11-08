// Package multiagent composes kritrim agents into graphs and swarms.
package multiagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ganeshdipdumbare/kritrim"
)

// State is shared by every node in a graph.
type State struct {
	mu     sync.Mutex
	values map[string]any
}

// NewState returns an empty graph state.
func NewState() *State { return &State{values: map[string]any{}} }

func (s *State) Get(key string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

func (s *State) Set(key string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = v
}

// Node runs one step of a graph.
type Node func(ctx context.Context, prompt string, state *State) (*kritrim.Result, error)

type edge struct {
	to   string
	cond func(*State) bool
}

// Graph is a developer-defined flowchart of agents or functions.
type Graph struct {
	nodes    map[string]Node
	edges    map[string][]edge
	start    string
	ends     map[string]bool
	maxSteps int
}

// NewGraph returns an empty graph.
func NewGraph() *Graph {
	return &Graph{nodes: map[string]Node{}, edges: map[string][]edge{}, ends: map[string]bool{}, maxSteps: 50}
}

// MaxSteps caps node executions. Default 50.
func (g *Graph) MaxSteps(n int) *Graph { g.maxSteps = n; return g }

// Node adds a named step.
func (g *Graph) Node(name string, fn Node) *Graph { g.nodes[name] = fn; return g }

// Edge adds a transition. A nil condition is unconditional.
func (g *Graph) Edge(from, to string, cond func(*State) bool) *Graph {
	g.edges[from] = append(g.edges[from], edge{to: to, cond: cond})
	return g
}

// Start sets the entry node.
func (g *Graph) Start(name string) *Graph { g.start = name; return g }

// End marks terminal nodes.
func (g *Graph) End(names ...string) *Graph {
	for _, n := range names {
		g.ends[n] = true
	}
	return g
}

// Run executes the graph and returns the final node's result. State carries
// data between nodes; the previous node's text is the next prompt.
func (g *Graph) Run(ctx context.Context, prompt string) (*kritrim.Result, error) {
	if g.start == "" {
		return nil, errors.New("multiagent: graph has no start node")
	}
	if len(g.nodes) == 0 {
		return nil, errors.New("multiagent: graph has no nodes")
	}
	st := NewState()
	cur := g.start
	next := prompt
	var last *kritrim.Result
	for i := 0; i < g.maxSteps; i++ {
		fn, ok := g.nodes[cur]
		if !ok {
			return nil, fmt.Errorf("multiagent: unknown node %q", cur)
		}
		res, err := fn(ctx, next, st)
		if err != nil {
			return nil, fmt.Errorf("multiagent: node %s: %w", cur, err)
		}
		last = res
		if res != nil && res.Stop == "interrupt" {
			return res, nil
		}
		if g.ends[cur] || len(g.edges[cur]) == 0 {
			return res, nil
		}
		to := ""
		for _, e := range g.edges[cur] {
			if e.cond == nil || e.cond(st) {
				to = e.to
				break
			}
		}
		if to == "" {
			return res, nil
		}
		next = ""
		if res != nil {
			next = res.Text
		}
		cur = to
	}
	return last, fmt.Errorf("multiagent: graph exceeded %d steps", g.maxSteps)
}

// Member is one agent in a swarm.
type Member struct {
	Name        string
	Description string
	Agent       *kritrim.Agent
}

// Swarm hands a task between members until one answers without handing off.
type Swarm struct {
	members     []Member
	maxHandoffs int
}

// NewSwarm validates the members.
func NewSwarm(members ...Member) (*Swarm, error) {
	if len(members) == 0 {
		return nil, errors.New("multiagent: swarm needs at least one member")
	}
	seen := map[string]bool{}
	for _, m := range members {
		if m.Name == "" || m.Agent == nil {
			return nil, errors.New("multiagent: each member needs a name and an agent")
		}
		if seen[m.Name] {
			return nil, fmt.Errorf("multiagent: duplicate member %q", m.Name)
		}
		seen[m.Name] = true
	}
	return &Swarm{members: members, maxHandoffs: 12}, nil
}

// MaxHandoffs caps the number of transitions.
func (s *Swarm) MaxHandoffs(n int) *Swarm { s.maxHandoffs = n; return s }

// Run starts with the first member and follows handoffs.
func (s *Swarm) Run(ctx context.Context, prompt string, opts ...kritrim.Option) (*kritrim.Result, error) {
	cur := 0
	next := prompt
	for i := 0; i <= s.maxHandoffs; i++ {
		m := s.members[cur]
		state := NewState()
		tools := s.handoffTools(cur, state)
		agent := m.Agent.WithTools(tools...)
		res, err := agent.Run(ctx, next, opts...)
		if err != nil {
			return nil, err
		}
		if res.Stop == "interrupt" {
			return res, nil
		}
		target, _ := state.Get("handoff").(string)
		if target == "" {
			return res, nil
		}
		idx := -1
		for j, x := range s.members {
			if x.Name == target {
				idx = j
			}
		}
		if idx < 0 {
			return res, nil
		}
		next = fmt.Sprintf("Task from %s:\n%s\n\nPrevious result:\n%s", m.Name, prompt, res.Text)
		cur = idx
	}
	return nil, fmt.Errorf("multiagent: swarm exceeded %d handoffs", s.maxHandoffs)
}

func (s *Swarm) handoffTools(from int, state *State) []kritrim.Tool {
	var out []kritrim.Tool
	for i, m := range s.members {
		if i == from {
			continue
		}
		name := m.Name
		desc := m.Description
		out = append(out, kritrim.NewTool("handoff_to_"+name, "Hand off to "+name+": "+desc,
			func(ctx context.Context, in struct {
				Task string `json:"task" desc:"what the next agent should do"`
			}) (string, error) {
				state.Set("handoff", name)
				if strings.TrimSpace(in.Task) != "" {
					state.Set("task", in.Task)
				}
				return "handed off to " + name, nil
			}))
	}
	return out
}
