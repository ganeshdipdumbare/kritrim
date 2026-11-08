package kritrim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Session is one stored conversation.
type Session struct {
	ID       string         `json:"id"`
	Messages []Message      `json:"messages"`
	Values   map[string]any `json:"values,omitempty"`
	Updated  time.Time      `json:"updated"`
}

// SessionStore loads and saves conversations by id.
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

func (s *memSessions) Load(ctx context.Context, id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[id]
	if !ok {
		return nil, nil
	}
	cp := *v
	cp.Messages = append([]Message(nil), v.Messages...)
	return &cp, nil
}

func (s *memSessions) Save(ctx context.Context, v *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *v
	cp.Messages = append([]Message(nil), v.Messages...)
	s.m[idOf(v)] = &cp
	return nil
}

// FileSessions stores one JSON file per session under dir.
func FileSessions(dir string) SessionStore { return &fileSessions{dir: dir} }

type fileSessions struct{ dir string }

func (s *fileSessions) path(id string) string {
	sum := sha256.Sum256([]byte(idOf(&Session{ID: id})))
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
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(idOf(v)) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(idOf(v)))
}

func idOf(v *Session) string {
	if v.ID == "" {
		return "default"
	}
	return v.ID
}
