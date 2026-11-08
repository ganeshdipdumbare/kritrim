// Package a2a implements the Agent-to-Agent protocol for kritrim agents:
// a client that calls remote agents and a server that exposes one.
//
// The implementation covers the core protocol: agent cards, message/send,
// message/stream, tasks/get, tasks/cancel, text and data parts, and the
// interrupt/resume flow. Push notifications, file parts and OAuth are not
// implemented.
package a2a

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Card is the agent card served at /.well-known/agent-card.json.
type Card struct {
	Name               string   `json:"name"`
	Description        string   `json:"description,omitempty"`
	URL                string   `json:"url,omitempty"`
	Version            string   `json:"version,omitempty"`
	ProtocolVersion    string   `json:"protocolVersion,omitempty"`
	PreferredTransport string   `json:"preferredTransport,omitempty"`
	Capabilities       Caps     `json:"capabilities"`
	DefaultInputModes  []string `json:"defaultInputModes,omitempty"`
	DefaultOutputModes []string `json:"defaultOutputModes,omitempty"`
	Skills             []Skill  `json:"skills,omitempty"`
}

// Caps advertises optional protocol features.
type Caps struct {
	Streaming              bool `json:"streaming"`
	PushNotifications      bool `json:"pushNotifications"`
	StateTransitionHistory bool `json:"stateTransitionHistory"`
}

// Skill is one capability advertised on the card.
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// Agent card paths. The older path is served as an alias.
const (
	CardPath        = "/.well-known/agent-card.json"
	OldCardPath     = "/.well-known/agent.json"
	DefaultCardPath = CardPath
)

// Task states.
const (
	StateSubmitted     = "submitted"
	StateWorking       = "working"
	StateInputRequired = "input-required"
	StateCompleted     = "completed"
	StateCanceled      = "canceled"
	StateFailed        = "failed"
)

// Part is a text or data content part.
type Part struct {
	Kind string         `json:"kind"`
	Text string         `json:"text,omitempty"`
	Data map[string]any `json:"data,omitempty"`
}

// TextPart builds a text part.
func TextPart(s string) Part { return Part{Kind: "text", Text: s} }

// DataPart builds a data part.
func DataPart(v map[string]any) Part { return Part{Kind: "data", Data: v} }

// Message is one A2A message.
type Message struct {
	Role      string `json:"role"`
	Parts     []Part `json:"parts"`
	MessageID string `json:"messageId"`
	TaskID    string `json:"taskId,omitempty"`
	ContextID string `json:"contextId,omitempty"`
}

// Text returns the concatenated text parts.
func (m Message) Text() string {
	s := ""
	for _, p := range m.Parts {
		if p.Kind == "text" {
			s += p.Text
		}
	}
	return s
}

// Task is one A2A task.
type Task struct {
	ID        string     `json:"id"`
	ContextID string     `json:"contextId,omitempty"`
	Status    TaskStatus `json:"status"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
	History   []Message  `json:"history,omitempty"`
}

// TaskStatus is the current state of a task.
type TaskStatus struct {
	State     string   `json:"state"`
	Message   *Message `json:"message,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"`
}

// Artifact is a produced result.
type Artifact struct {
	ArtifactID string `json:"artifactId"`
	Name       string `json:"name,omitempty"`
	Parts      []Part `json:"parts"`
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
