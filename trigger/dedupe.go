package trigger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

// DedupeStore remembers deliveries so each one runs once: webhook
// deliveries across handlers, and scheduled ticks across schedulers. The
// default is in memory, which is enough for one process; when several
// instances share the traffic, give them one SQLDedupe.
type DedupeStore interface {
	// Claim registers id, with a fingerprint of its payload, until
	// expires. When the result has Won set, the caller runs the delivery
	// and must Complete or Release it. A reused id with a different
	// fingerprint is ErrConflict; a store with no room is ErrBusy.
	Claim(ctx context.Context, id, fingerprint string, expires time.Time) (Claim, error)
	// Complete records the result of a delivery this caller won.
	Complete(ctx context.Context, id string, result []byte) error
	// Release forgets a delivery this caller won but could not finish, so
	// a retry runs it.
	Release(ctx context.Context, id string) error
}

// Claim is the outcome of DedupeStore.Claim. With neither Won nor Done set,
// the delivery is running elsewhere.
type Claim struct {
	Won    bool
	Done   bool
	Result []byte
}

var (
	// ErrConflict means an idempotency key was reused for a different
	// payload.
	ErrConflict = errors.New("trigger: idempotency key reused for a different payload")
	// ErrBusy means the store is full of deliveries that are running or
	// can still be replayed; the sender should retry later.
	ErrBusy = errors.New("trigger: dedupe store is full")
)

// MemoryDedupe keeps deliveries in memory, up to maxEntries (default
// 100000). It never forgets a delivery that is running or unexpired: when
// it is full of them, Claim returns ErrBusy instead.
func MemoryDedupe(maxEntries int) DedupeStore {
	if maxEntries <= 0 {
		maxEntries = 100_000
	}
	return &memDedupe{max: maxEntries, m: map[string]*memRecord{}}
}

type memRecord struct {
	print   string
	done    bool
	result  []byte
	expires time.Time
}

type memDedupe struct {
	mu  sync.Mutex
	max int
	m   map[string]*memRecord
}

func (s *memDedupe) Claim(ctx context.Context, id, print string, expires time.Time) (Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	// A claim is live until it expires. An expired one that never finished
	// (its leader hung or died) is taken over, as the SQL store does.
	if r, ok := s.m[id]; ok && now.Before(r.expires) {
		if r.print != print {
			return Claim{}, ErrConflict
		}
		return Claim{Done: r.done, Result: r.result}, nil
	}
	if len(s.m) >= s.max {
		for k, r := range s.m {
			if now.After(r.expires) {
				delete(s.m, k)
			}
		}
		if len(s.m) >= s.max {
			return Claim{}, ErrBusy
		}
	}
	s.m[id] = &memRecord{print: print, expires: expires}
	return Claim{Won: true}, nil
}

// fenceKey carries the expiry of the claim a leader holds. Release and Complete
// act only on the claim with that expiry, so a leader whose claim expired
// cannot delete or finish a newer leader's claim.
type fenceKey struct{}

func withFence(ctx context.Context, expires time.Time) context.Context {
	return context.WithValue(ctx, fenceKey{}, expires.UnixMilli())
}

// fenced reports whether ctx holds the claim with this expiry, or no fence.
func fenced(ctx context.Context, expires time.Time) bool {
	f, ok := ctx.Value(fenceKey{}).(int64)
	return !ok || f == expires.UnixMilli()
}

func (s *memDedupe) Complete(ctx context.Context, id string, result []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.m[id]; ok && fenced(ctx, r.expires) {
		r.done, r.result = true, result
	}
	return nil
}

func (s *memDedupe) Release(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.m[id]; ok && !r.done && fenced(ctx, r.expires) {
		delete(s.m, id)
	}
	return nil
}

// SQLConfig configures SQLDedupe.
type SQLConfig struct {
	// Table defaults to kritrim_dedupe.
	Table string
	// Dialect is "postgres" (default), "sqlite" or "mysql".
	Dialect string
}

var validTable = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SQLDedupe keeps deliveries in a SQL table shared by every instance. The
// app imports its driver. A claim is a unique-key insert, so two instances
// racing for one delivery cannot both win. A delivery whose instance died
// before finishing can be claimed again once it expires.
func SQLDedupe(db *sql.DB, cfg SQLConfig) (DedupeStore, error) {
	if cfg.Table == "" {
		cfg.Table = "kritrim_dedupe"
	}
	if !validTable.MatchString(cfg.Table) {
		return nil, fmt.Errorf("trigger: invalid table %q", cfg.Table)
	}
	switch cfg.Dialect {
	case "":
		cfg.Dialect = "postgres"
	case "postgres", "sqlite", "mysql":
	default:
		return nil, fmt.Errorf("trigger: unknown dialect %q", cfg.Dialect)
	}
	s := &sqlDedupe{db: db, cfg: cfg}
	ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id VARCHAR(255) PRIMARY KEY,
		fingerprint VARCHAR(128) NOT NULL,
		done INTEGER NOT NULL,
		result TEXT NOT NULL,
		expires BIGINT NOT NULL
	)`, cfg.Table)
	if _, err := db.ExecContext(context.Background(), ddl); err != nil {
		return nil, err
	}
	return s, nil
}

type sqlDedupe struct {
	db  *sql.DB
	cfg SQLConfig

	mu    sync.Mutex
	swept time.Time
}

func (s *sqlDedupe) q(query string) string {
	if s.cfg.Dialect != "postgres" {
		return query
	}
	out := make([]byte, 0, len(query)+8)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			out = append(out, fmt.Sprintf("$%d", n)...)
			continue
		}
		out = append(out, query[i])
	}
	return string(out)
}

// key keeps ids within the column size.
func (s *sqlDedupe) key(id string) string {
	if len(id) <= 200 {
		return id
	}
	h := sha256.Sum256([]byte(id))
	return "sha256#" + hex.EncodeToString(h[:])
}

func (s *sqlDedupe) Claim(ctx context.Context, id, print string, expires time.Time) (Claim, error) {
	id = s.key(id)
	now := time.Now()
	s.sweep(ctx, now)
	if _, err := s.db.ExecContext(ctx, s.q(fmt.Sprintf("DELETE FROM %s WHERE id = ? AND expires < ?", s.cfg.Table)), id, now.UnixMilli()); err != nil {
		return Claim{}, err
	}
	_, insErr := s.db.ExecContext(ctx, s.q(fmt.Sprintf("INSERT INTO %s (id, fingerprint, done, result, expires) VALUES (?, ?, 0, '', ?)", s.cfg.Table)),
		id, print, expires.UnixMilli())
	if insErr == nil {
		return Claim{Won: true}, nil
	}
	var fp, result string
	var done int
	err := s.db.QueryRowContext(ctx, s.q(fmt.Sprintf("SELECT fingerprint, done, result FROM %s WHERE id = ?", s.cfg.Table)), id).Scan(&fp, &done, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, insErr
	}
	if err != nil {
		return Claim{}, err
	}
	if fp != print {
		return Claim{}, ErrConflict
	}
	if done == 1 {
		return Claim{Done: true, Result: []byte(result)}, nil
	}
	return Claim{}, nil
}

// sweep deletes expired rows at most once a minute, so the table does not
// grow without bound.
func (s *sqlDedupe) sweep(ctx context.Context, now time.Time) {
	s.mu.Lock()
	if now.Sub(s.swept) < time.Minute {
		s.mu.Unlock()
		return
	}
	s.swept = now
	s.mu.Unlock()
	_, _ = s.db.ExecContext(ctx, s.q(fmt.Sprintf("DELETE FROM %s WHERE expires < ? AND done = 1", s.cfg.Table)), now.UnixMilli())
}

func (s *sqlDedupe) Complete(ctx context.Context, id string, result []byte) error {
	if f, ok := ctx.Value(fenceKey{}).(int64); ok {
		_, err := s.db.ExecContext(ctx, s.q(fmt.Sprintf("UPDATE %s SET done = 1, result = ? WHERE id = ? AND expires = ?", s.cfg.Table)), string(result), s.key(id), f)
		return err
	}
	_, err := s.db.ExecContext(ctx, s.q(fmt.Sprintf("UPDATE %s SET done = 1, result = ? WHERE id = ?", s.cfg.Table)), string(result), s.key(id))
	return err
}

func (s *sqlDedupe) Release(ctx context.Context, id string) error {
	if f, ok := ctx.Value(fenceKey{}).(int64); ok {
		_, err := s.db.ExecContext(ctx, s.q(fmt.Sprintf("DELETE FROM %s WHERE id = ? AND done = 0 AND expires = ?", s.cfg.Table)), s.key(id), f)
		return err
	}
	_, err := s.db.ExecContext(ctx, s.q(fmt.Sprintf("DELETE FROM %s WHERE id = ? AND done = 0", s.cfg.Table)), s.key(id))
	return err
}
