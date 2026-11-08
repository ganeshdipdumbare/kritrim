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

func copySession(v *Session) *Session {
	cp := *v
	cp.Messages = append([]Message(nil), v.Messages...)
	cp.Pending = append([]Interrupt(nil), v.Pending...)
	if v.Values != nil {
		cp.Values = make(map[string]any, len(v.Values))
		for k, x := range v.Values {
			cp.Values[k] = x
		}
	}
	if v.Paused != nil {
		p := *v.Paused
		p.Interrupts = append([]Interrupt(nil), v.Paused.Interrupts...)
		p.Calls = append([]ToolCall(nil), v.Paused.Calls...)
		p.Results = append([]ToolResult(nil), v.Paused.Results...)
		cp.Paused = &p
	}
	return &cp
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
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(idOf(v.ID)) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(idOf(v.ID)))
}

func idOf(id string) string {
	if id == "" {
		return "default"
	}
	return id
}
