// Package memory gives agents a small long-term store and remember/recall
// tools. It is lexical, not a vector database.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// Item is one memory.
type Item struct {
	Key     string    `json:"key"`
	Value   string    `json:"value"`
	Updated time.Time `json:"updated"`
}

// Store is the memory backend.
type Store interface {
	Put(ctx context.Context, key, value string) error
	Get(ctx context.Context, key string) (string, bool)
	Search(ctx context.Context, query string, limit int) ([]Item, error)
	Delete(ctx context.Context, key string) error
}

// NewMemory returns an in-memory store.
func NewMemory() Store { return &memStore{m: map[string]Item{}} }

type memStore struct {
	mu sync.RWMutex
	m  map[string]Item
}

func (s *memStore) Put(ctx context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = Item{Key: key, Value: value, Updated: time.Now()}
	return nil
}

func (s *memStore) Get(ctx context.Context, key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[key]
	return v.Value, ok
}

func (s *memStore) Search(ctx context.Context, query string, limit int) ([]Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return search(s.m, query, limit), nil
}

func (s *memStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

// NewFile stores memories in one JSON file under dir.
func NewFile(dir string) (Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &fileStore{path: filepath.Join(dir, "memory.json"), m: map[string]Item{}}
	b, err := os.ReadFile(s.path)
	if err == nil {
		_ = json.Unmarshal(b, &s.m)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

type fileStore struct {
	mu   sync.Mutex
	path string
	m    map[string]Item
}

func (s *fileStore) Put(ctx context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = Item{Key: key, Value: value, Updated: time.Now()}
	return s.flush()
}

func (s *fileStore) Get(ctx context.Context, key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	return v.Value, ok
}

func (s *fileStore) Search(ctx context.Context, query string, limit int) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return search(s.m, query, limit), nil
}

func (s *fileStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return s.flush()
}

func (s *fileStore) flush() error {
	b, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func search(m map[string]Item, query string, limit int) []Item {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []Item
	for _, it := range m {
		score := 0
		if q == "" {
			score = 1
		} else {
			score = strings.Count(strings.ToLower(it.Key), q)*2 + strings.Count(strings.ToLower(it.Value), q)
		}
		if score > 0 {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Updated.Equal(out[j].Updated) {
			return out[i].Key < out[j].Key
		}
		return out[i].Updated.After(out[j].Updated)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Tools returns remember and recall tools backed by s.
func Tools(s Store) []kritrim.Tool {
	remember := kritrim.NewTool("remember", "Store a durable fact for later recall.",
		func(ctx context.Context, in struct {
			Key   string `json:"key" desc:"short topic key"`
			Value string `json:"value" desc:"the fact to remember"`
		}) (string, error) {
			if in.Key == "" {
				return "", fmt.Errorf("key is required")
			}
			if err := s.Put(ctx, in.Key, in.Value); err != nil {
				return "", err
			}
			return "remembered " + in.Key, nil
		})
	recall := kritrim.NewTool("recall", "Recall durable facts matching a query.",
		func(ctx context.Context, in struct {
			Query string `json:"query" desc:"what to look for"`
			Limit int    `json:"limit,omitempty" desc:"max results (default 5)"`
		}) (string, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 5
			}
			items, err := s.Search(ctx, in.Query, limit)
			if err != nil {
				return "", err
			}
			if len(items) == 0 {
				return "no memories found", nil
			}
			var b strings.Builder
			for _, it := range items {
				fmt.Fprintf(&b, "%s: %s\n", it.Key, it.Value)
			}
			return strings.TrimSpace(b.String()), nil
		})
	return []kritrim.Tool{remember, recall}
}
