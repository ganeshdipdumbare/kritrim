package kritrim

import (
	"context"
	"sync"
)

// Turn is one scripted mock model reply.
type Turn struct {
	Text      string
	Reasoning string
	ToolCalls []ToolCall
	Usage     Usage
	Stop      string
	Err       error
}

// Mock returns an offline model that replays turns in order. With no turns
// it answers with a fixed greeting.
func Mock(turns ...Turn) Model {
	if len(turns) == 0 {
		turns = []Turn{{Text: "Hello from the mock model."}}
	}
	return &mockModel{turns: turns}
}

type mockModel struct {
	mu    sync.Mutex
	turns []Turn
}

func (m *mockModel) next() (Turn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) == 0 {
		return Turn{Text: "(mock: no turns left)"}, nil
	}
	t := m.turns[0]
	if len(m.turns) > 1 {
		m.turns = m.turns[1:]
	}
	return t, t.Err
}

func (m *mockModel) Generate(ctx context.Context, req Request) (*Response, error) {
	return m.response()
}

func (m *mockModel) Stream(ctx context.Context, req Request, emit func(Delta)) (*Response, error) {
	resp, err := m.response()
	if err == nil && resp.Text != "" {
		emit(Delta{Text: resp.Text})
	}
	return resp, err
}

func (m *mockModel) response() (*Response, error) {
	t, err := m.next()
	if err != nil {
		return nil, err
	}
	stop := t.Stop
	if stop == "" {
		if len(t.ToolCalls) > 0 {
			stop = "tool_calls"
		} else {
			stop = "stop"
		}
	}
	return &Response{Text: t.Text, Reasoning: t.Reasoning, ToolCalls: t.ToolCalls,
		Usage: t.Usage, Stop: stop}, nil
}

// TurnTool is shorthand for a scripted tool call.
func TurnTool(name string, args map[string]any) Turn {
	return Turn{ToolCalls: []ToolCall{{ID: "call_" + name, Name: name, Args: args}}}
}
