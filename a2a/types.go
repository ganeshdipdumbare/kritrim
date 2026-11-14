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

// Part kinds, the values of Part.Kind.
const (
	KindText = "text"
	KindData = "data"
	KindFile = "file"
)

// Part is a text, data or file content part.
type Part struct {
	Kind string         `json:"kind"`
	Text string         `json:"text,omitempty"`
	Data map[string]any `json:"data,omitempty"`
	File *File          `json:"file,omitempty"`
}

// File is the content of a file part: inline Bytes (base64 on the wire) or
// a URI, never both.
type File struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Bytes    []byte `json:"bytes,omitempty"`
	URI      string `json:"uri,omitempty"`
}

// TextPart builds a text part.
func TextPart(s string) Part { return Part{Kind: KindText, Text: s} }

// DataPart builds a data part.
func DataPart(v map[string]any) Part { return Part{Kind: KindData, Data: v} }

// FilePart builds a file part with inline bytes.
func FilePart(name, mimeType string, data []byte) Part {
	return Part{Kind: KindFile, File: &File{Name: name, MimeType: mimeType, Bytes: data}}
}

// FileURI builds a file part that points at a URI. A kritrim server
// fetches it only from a host in ServerConfig.FileHosts.
func FileURI(name, mimeType, uri string) Part {
	return Part{Kind: KindFile, File: &File{Name: name, MimeType: mimeType, URI: uri}}
}

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
		if p.Kind == KindText {
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
	// Owner is the principal that created the task when Auth implements
	// Principal. Only that caller can read, continue, cancel or subscribe
	// to it.
	Owner string `json:"owner,omitempty"`
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
