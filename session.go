package kritrim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrSessionConflict means the session changed since it was loaded.
var ErrSessionConflict = errors.New("kritrim: session changed concurrently")

// Session is one stored conversation and its paused state.
type Session struct {
	ID       string         `json:"id"`
	Messages []Message      `json:"messages"`
	Values   map[string]any `json:"values,omitempty"`
	Pending  []Interrupt    `json:"pending,omitempty"`
	Paused   *PausedStep    `json:"paused,omitempty"`
	Version  int64          `json:"version"`
	Updated  time.Time      `json:"updated"`
	// Sig is set by SignedSessions and is not part of what it signs.
	Sig string `json:"sig,omitempty"`
}

// SessionStore loads and saves conversations. Save is compare-and-swap on
// Session.Version and returns ErrSessionConflict when another writer won.
type SessionStore interface {
	Load(ctx context.Context, id string) (*Session, error)
	Save(ctx context.Context, s *Session) error
}

// MemorySessions keeps sessions in memory. This is the default.
func MemorySessions() SessionStore { return &memSessions{m: map[string]*Session{}} }

type memSessions struct {
	mu sync.RWMutex
	m  map[string]*Session
}

// copySession returns a copy a caller can change freely. Nested maps and
// slices are copied too: a hook or tool that edits ToolCall.Args must not
// edit the stored session, and a shared map read while another run writes it
// is a fatal error in Go, which recover cannot catch.
func copySession(v *Session) *Session {
	cp := *v
	cp.Messages = copyMessages(v.Messages)
	cp.Pending = make([]Interrupt, len(v.Pending))
	for i, in := range v.Pending {
		cp.Pending[i] = copyInterrupt(in)
	}
	if v.Values != nil {
		cp.Values = make(map[string]any, len(v.Values))
		for k, x := range v.Values {
			cp.Values[k] = deepCopyAny(x)
		}
	}
	if v.Paused != nil {
		p := *v.Paused
		p.Assistant = copyMessage(v.Paused.Assistant)
		p.Calls = copyCalls(v.Paused.Calls)
		p.Results = append([]ToolResult(nil), v.Paused.Results...)
		p.Interrupts = make([]Interrupt, len(v.Paused.Interrupts))
		for i, in := range v.Paused.Interrupts {
			p.Interrupts[i] = copyInterrupt(in)
		}
		cp.Paused = &p
	}
	return &cp
}

func copyMessages(in []Message) []Message {
	if in == nil {
		return nil
	}
	out := make([]Message, len(in))
	for i, m := range in {
		out[i] = copyMessage(m)
	}
	return out
}

func copyMessage(m Message) Message {
	m.ToolCalls = copyCalls(m.ToolCalls)
	if m.Images != nil {
		m.Images = make([]Image, len(m.Images))
		for i, img := range m.Images {
			img.Data = append([]byte(nil), img.Data...)
			m.Images[i] = img
		}
	}
	return m
}

func copyCalls(in []ToolCall) []ToolCall {
	if in == nil {
		return nil
	}
	out := make([]ToolCall, len(in))
	for i, c := range in {
		c.Args = deepCopyAny(c.Args).(map[string]any)
		out[i] = c
	}
	return out
}

func copyInterrupt(in Interrupt) Interrupt {
	if in.Args != nil {
		in.Args = deepCopyAny(in.Args).(map[string]any)
	}
	return in
}

// deepCopyAny copies the maps and slices inside a JSON-like value. Other
// values are immutable here and are returned as they are.
func deepCopyAny(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if x == nil {
			return x
		}
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = deepCopyAny(e)
		}
		return m
	case []any:
		if x == nil {
			return x
		}
		s := make([]any, len(x))
		for i, e := range x {
			s[i] = deepCopyAny(e)
		}
		return s
	case []string:
		return append([]string(nil), x...)
	case map[string]string:
		if x == nil {
			return x
		}
		m := make(map[string]string, len(x))
		for k, e := range x {
			m[k] = e
		}
		return m
	default:
		return v
	}
}

func (s *memSessions) Load(ctx context.Context, id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[idOf(id)]
	if !ok {
		return nil, nil
	}
	return copySession(v), nil
}

func (s *memSessions) Save(ctx context.Context, v *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := idOf(v.ID)
	cur := s.m[id]
	if cur == nil {
		if v.Version != 0 {
			return ErrSessionConflict
		}
		v.Version = 1
	} else {
		if cur.Version != v.Version {
			return ErrSessionConflict
		}
		v.Version = cur.Version + 1
	}
	v.Updated = now()
	s.m[id] = copySession(v)
	return nil
}

// FileSessions stores one JSON file per session under dir.
func FileSessions(dir string) SessionStore { return &fileSessions{dir: dir} }

type fileSessions struct{ dir string }

func (s *fileSessions) path(id string) string {
	sum := sha256.Sum256([]byte(idOf(id)))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:16])+".json")
}

func (s *fileSessions) Load(ctx context.Context, id string) (*Session, error) {
	b, err := os.ReadFile(s.path(id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v Session
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("kritrim: session %s: %w", id, err)
	}
	return &v, nil
}

func (s *fileSessions) Save(ctx context.Context, v *Session) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	// The lock covers the version check through the rename, so two writers
	// cannot both pass the check on the same version. It is taken on the
	// directory, which excludes other goroutines and processes and creates no
	// extra files. Saves to one directory are serialized, which is brief.
	dirLock, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dirLock.Close()
	unlock, err := lockFile(dirLock)
	if err != nil {
		return err
	}
	defer unlock()

	cur, err := s.Load(ctx, idOf(v.ID))
	if err != nil {
		return err
	}
	switch {
	case cur == nil && v.Version != 0:
		return ErrSessionConflict
	case cur != nil && cur.Version != v.Version:
		return ErrSessionConflict
	}
	if cur == nil {
		v.Version = 1
	} else {
		v.Version = cur.Version + 1
	}
	v.Updated = now()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// A unique temp file per write, so writers never share one.
	tmp, err := os.CreateTemp(s.dir, filepath.Base(s.path(idOf(v.ID)))+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(idOf(v.ID)))
}

func idOf(id string) string {
	if id == "" {
		return "default"
	}
	return id
}
