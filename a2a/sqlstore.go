package a2a

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
)

// SQLConfig configures a SQL task store.
type SQLConfig struct {
	// Table defaults to kritrim_a2a_tasks.
	Table string
	// Dialect is "postgres" (default), "sqlite" or "mysql".
	Dialect string
}

// SQLTasks stores A2A tasks in a SQL table. The app imports its driver.
func SQLTasks(db *sql.DB, cfg SQLConfig) (TaskStore, error) {
	if cfg.Table == "" {
		cfg.Table = "kritrim_a2a_tasks"
	}
	if !validTable.MatchString(cfg.Table) {
		return nil, fmt.Errorf("a2a: invalid table %q", cfg.Table)
	}
	if cfg.Dialect == "" {
		cfg.Dialect = "postgres"
	}
	s := &sqlTasks{db: db, cfg: cfg}
	if err := s.init(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

var validTable = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type sqlTasks struct {
	db  *sql.DB
	cfg SQLConfig
}

func (s *sqlTasks) init(ctx context.Context) error {
	col := "JSONB"
	if s.cfg.Dialect != "postgres" {
		col = "TEXT"
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (id TEXT PRIMARY KEY, data %s NOT NULL)`, s.cfg.Table, col))
	return err
}

func (s *sqlTasks) Get(ctx context.Context, id string) (*Task, error) {
	ph := "?"
	if s.cfg.Dialect == "postgres" {
		ph = "$1"
	}
	var data []byte
	err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT data FROM %s WHERE id = %s", s.cfg.Table, ph), id).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var t Task
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *sqlTasks) Put(ctx context.Context, t *Task) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	var q string
	switch s.cfg.Dialect {
	case "mysql":
		q = fmt.Sprintf(`INSERT INTO %s (id, data) VALUES (?, ?) ON DUPLICATE KEY UPDATE data = VALUES(data)`, s.cfg.Table)
	case "sqlite":
		q = fmt.Sprintf(`INSERT INTO %s (id, data) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, s.cfg.Table)
	default:
		q = fmt.Sprintf(`INSERT INTO %s (id, data) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data`, s.cfg.Table)
	}
	_, err = s.db.ExecContext(ctx, q, t.ID, string(b))
	return err
}
