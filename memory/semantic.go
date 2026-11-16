package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Embedder turns texts into vectors. Every vector it returns for one model
// has the same length.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// SemanticConfig configures NewSemantic.
type SemanticConfig struct {
	// Dir keeps the vectors in Dir/vectors.json so they survive a restart
	// without re-embedding. Empty keeps them in memory.
	Dir string
	// Lexical is the weight of the keyword score in the ranking, 0 to 1.
	// Default 0.2: meaning ranks first, an exact term still helps.
	Lexical float64
	// MinScore drops results below this blended score. Default 0.
	MinScore float64
}

// NewSemantic adds embedding search to a store. Put embeds the memory,
// Search ranks every memory by cosine similarity to the query blended with
// the keyword score, and Get and Delete pass through. base must list every
// memory for an empty query, as NewMemory and NewFile do. Memories that
// were stored before NewSemantic wrapped the store are embedded on the
// first search.
//
// Embeddings are a network call to a model provider: what you remember is
// sent to it.
func NewSemantic(base Store, emb Embedder, cfg SemanticConfig) (Store, error) {
	if base == nil || emb == nil {
		return nil, errors.New("memory: NewSemantic needs a store and an embedder")
	}
	if cfg.Lexical < 0 || cfg.Lexical > 1 {
		return nil, errors.New("memory: SemanticConfig.Lexical must be between 0 and 1")
	}
	if cfg.Lexical == 0 {
		cfg.Lexical = 0.2
	}
	s := &semantic{base: base, emb: emb, cfg: cfg, vecs: map[string]vector{}}
	if cfg.Dir != "" {
		if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
			return nil, err
		}
		s.path = filepath.Join(cfg.Dir, "vectors.json")
		b, err := os.ReadFile(s.path)
		switch {
		case err == nil:
			if err := json.Unmarshal(b, &s.vecs); err != nil {
				return nil, fmt.Errorf("memory: %s: %w", s.path, err)
			}
		case !os.IsNotExist(err):
			return nil, err
		}
	}
	return s, nil
}

type vector struct {
	// Sum identifies the text the vector was made from, so an edited
	// memory is re-embedded.
	Sum string    `json:"sum"`
	V   []float32 `json:"v"`
}

type semantic struct {
	base Store
	emb  Embedder
	cfg  SemanticConfig
	path string

	mu   sync.Mutex
	vecs map[string]vector
}

func memoryText(key, value string) string { return key + ": " + value }

func textSum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:12])
}

func (s *semantic) Put(ctx context.Context, key, value string) error {
	text := memoryText(key, value)
	vs, err := s.emb.Embed(ctx, []string{text})
	if err != nil {
		return fmt.Errorf("memory: embed: %w", err)
	}
	if len(vs) != 1 {
		return errors.New("memory: embedder returned the wrong number of vectors")
	}
	if err := s.base.Put(ctx, key, value); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vecs[key] = vector{Sum: textSum(text), V: vs[0]}
	return s.flush()
}

func (s *semantic) Get(ctx context.Context, key string) (string, bool) { return s.base.Get(ctx, key) }

func (s *semantic) Delete(ctx context.Context, key string) error {
	if err := s.base.Delete(ctx, key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.vecs, key)
	return s.flush()
}

func (s *semantic) Search(ctx context.Context, query string, limit int) ([]Item, error) {
	items, err := s.base.Search(ctx, "", 0)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" || len(items) == 0 {
		if limit > 0 && len(items) > limit {
			items = items[:limit]
		}
		return items, nil
	}
	if err := s.embedMissing(ctx, items); err != nil {
		return nil, err
	}
	qv, err := s.emb.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("memory: embed: %w", err)
	}
	if len(qv) != 1 {
		return nil, errors.New("memory: embedder returned the wrong number of vectors")
	}
	terms := strings.Fields(strings.ToLower(query))
	type scored struct {
		it    Item
		score float64
	}
	var out []scored
	s.mu.Lock()
	for _, it := range items {
		v, ok := s.vecs[it.Key]
		if !ok {
			continue
		}
		score := (1-s.cfg.Lexical)*cosine(qv[0], v.V) + s.cfg.Lexical*lexical(terms, it)
		if score >= s.cfg.MinScore {
			out = append(out, scored{it, score})
		}
	}
	s.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].it.Key < out[j].it.Key
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	res := make([]Item, len(out))
	for i, o := range out {
		res[i] = o.it
	}
	return res, nil
}

// embedMissing embeds memories with no vector, or a vector made from text
// that has since changed, in one batch.
func (s *semantic) embedMissing(ctx context.Context, items []Item) error {
	var keys, texts []string
	// seen records the vector each key had when we decided to embed it.
	seen := map[string]*vector{}
	s.mu.Lock()
	for _, it := range items {
		text := memoryText(it.Key, it.Value)
		v, ok := s.vecs[it.Key]
		if !ok || v.Sum != textSum(text) {
			keys, texts = append(keys, it.Key), append(texts, text)
			if ok {
				c := v
				seen[it.Key] = &c
			} else {
				seen[it.Key] = nil
			}
		}
	}
	s.mu.Unlock()
	if len(texts) == 0 {
		return nil
	}
	vs, err := s.emb.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("memory: embed: %w", err)
	}
	if len(vs) != len(texts) {
		return errors.New("memory: embedder returned the wrong number of vectors")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, k := range keys {
		// A Put that ran while we embedded already wrote a vector for newer
		// text. Ours is stale: keep theirs.
		cur, ok := s.vecs[k]
		old := seen[k]
		if (old == nil) != !ok || (ok && old != nil && cur.Sum != old.Sum) {
			continue
		}
		s.vecs[k] = vector{Sum: textSum(texts[i]), V: vs[i]}
	}
	return s.flush()
}

func (s *semantic) flush() error {
	if s.path == "" {
		return nil
	}
	b, err := json.Marshal(s.vecs)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// lexical is the share of query terms that appear in the memory.
func lexical(terms []string, it Item) float64 {
	if len(terms) == 0 {
		return 0
	}
	text := strings.ToLower(it.Key + " " + it.Value)
	hits := 0
	for _, t := range terms {
		if strings.Contains(text, t) {
			hits++
		}
	}
	return float64(hits) / float64(len(terms))
}
