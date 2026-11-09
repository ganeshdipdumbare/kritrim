package kritrim

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// SQLConfig configures a SQL session store.
type SQLConfig struct {
	// Table defaults to kritrim_sessions.
	Table string
	// Dialect is "postgres" (default), "sqlite" or "mysql". It selects the
	// placeholder style and column types.
	Dialect string
}

// SQLSessions stores sessions in a SQL table. The app imports its driver;
// kritrim has no database dependency.
//
//	store, err := kritrim.SQLSessions(db, kritrim.SQLConfig{})
func SQLSessions(db *sql.DB, cfg SQLConfig) (SessionStore, error) {
	if cfg.Table == "" {
		cfg.Table = "kritrim_sessions"
	}
	if !validTable.MatchString(cfg.Table) {
		return nil, fmt.Errorf("kritrim: invalid table %q", cfg.Table)
	}
	if cfg.Dialect == "" {
		cfg.Dialect = "postgres"
	}
	s := &sqlSessions{db: db, cfg: cfg}
	if err := s.init(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

var validTable = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type sqlSessions struct {
	db  *sql.DB
	cfg SQLConfig
}

func (s *sqlSessions) ph(n int) string {
	if s.cfg.Dialect == "postgres" {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

func (s *sqlSessions) init(ctx context.Context) error {
	var ddl string
	switch s.cfg.Dialect {
	case "postgres":
		ddl = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			version BIGINT NOT NULL,
			updated TIMESTAMPTZ NOT NULL,
			data JSONB NOT NULL
		)`, s.cfg.Table)
	default:
		ddl = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			version BIGINT NOT NULL,
			updated TEXT NOT NULL,
			data TEXT NOT NULL
		)`, s.cfg.Table)
	}
	_, err := s.db.ExecContext(ctx, ddl)
	return err
}

func (s *sqlSessions) Load(ctx context.Context, id string) (*Session, error) {
	q := fmt.Sprintf("SELECT version, data FROM %s WHERE id = %s", s.cfg.Table, s.ph(1))
	var version int64
	var data []byte
	err := s.db.QueryRowContext(ctx, q, idOf(id)).Scan(&version, &data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v Session
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	v.Version = version
	return &v, nil
}

func (s *sqlSessions) Save(ctx context.Context, v *Session) error {
	v.Updated = now()
	id := idOf(v.ID)
	if v.Version == 0 {
		q := fmt.Sprintf("INSERT INTO %s (id, version, updated, data) VALUES (%s, %s, %s, %s)",
			s.cfg.Table, s.ph(1), s.ph(2), s.ph(3), s.ph(4))
		if _, err := s.db.ExecContext(ctx, q, id, 1, v.Updated, string(mustJSON(v))); err != nil {
			if isDuplicate(err) {
				return ErrSessionConflict
			}
			return err
		}
		v.Version = 1
		return nil
	}
	q := fmt.Sprintf("UPDATE %s SET version = version + 1, updated = %s, data = %s WHERE id = %s AND version = %s",
		s.cfg.Table, s.ph(1), s.ph(2), s.ph(3), s.ph(4))
	res, err := s.db.ExecContext(ctx, q, v.Updated, string(mustJSON(v)), id, v.Version)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSessionConflict
	}
	v.Version++
	return nil
}

func mustJSON(v *Session) []byte {
	b, _ := json.Marshal(v)
	return b
}

func isDuplicate(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "duplicate") || strings.Contains(s, "unique") || strings.Contains(s, "constraint")
}
