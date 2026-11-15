package trigger

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ganeshdipdumbare/kritrim"
)

// fakeDB is a database/sql driver that understands exactly the statements
// SQLDedupe issues, enough to test it without a database dependency.
type fakeDB struct {
	mu   sync.Mutex
	rows map[string]*fakeRow
}

type fakeRow struct {
	fp      string
	done    int64
	result  string
	expires int64
}

var (
	fakeDBs   sync.Map
	fakeCount atomic.Int32
)

func init() { sql.Register("kritrimfake", fakeDriver{}) }

type fakeDriver struct{}

func (fakeDriver) Open(name string) (driver.Conn, error) {
	db, _ := fakeDBs.LoadOrStore(name, &fakeDB{rows: map[string]*fakeRow{}})
	return &fakeConn{db: db.(*fakeDB)}, nil
}

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{db: c.db, q: q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { return nil, errors.New("no transactions") }

type fakeStmt struct {
	db *fakeDB
	q  string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }

type fakeResult int64

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return int64(r), nil }

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	db := s.db
	db.mu.Lock()
	defer db.mu.Unlock()
	q := strings.Join(strings.Fields(s.q), " ")
	switch {
	case strings.HasPrefix(q, "CREATE TABLE"):
		return fakeResult(0), nil
	case strings.Contains(q, "WHERE id = ? AND expires < ?"):
		id, now := args[0].(string), args[1].(int64)
		if r, ok := db.rows[id]; ok && r.expires < now {
			delete(db.rows, id)
			return fakeResult(1), nil
		}
		return fakeResult(0), nil
	case strings.Contains(q, "WHERE expires < ? AND done = 1"):
		n := 0
		for k, r := range db.rows {
			if r.expires < args[0].(int64) && r.done == 1 {
				delete(db.rows, k)
				n++
			}
		}
		return fakeResult(n), nil
	case strings.HasPrefix(q, "INSERT"):
		id := args[0].(string)
		if _, ok := db.rows[id]; ok {
			return nil, errors.New("UNIQUE constraint failed: id")
		}
		db.rows[id] = &fakeRow{fp: args[1].(string), expires: args[2].(int64)}
		return fakeResult(1), nil
	case strings.HasPrefix(q, "UPDATE"):
		if r, ok := db.rows[args[1].(string)]; ok {
			r.done, r.result = 1, args[0].(string)
			return fakeResult(1), nil
		}
		return fakeResult(0), nil
	case strings.Contains(q, "WHERE id = ? AND done = 0"):
		if r, ok := db.rows[args[0].(string)]; ok && r.done == 0 {
			delete(db.rows, args[0].(string))
			return fakeResult(1), nil
		}
		return fakeResult(0), nil
	}
	return nil, errors.New("fake: unsupported exec: " + q)
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	if !strings.HasPrefix(s.q, "SELECT fingerprint, done, result") {
		return nil, errors.New("fake: unsupported query: " + s.q)
	}
	r, ok := s.db.rows[args[0].(string)]
	if !ok {
		return &fakeRows{}, nil
	}
	return &fakeRows{vals: [][]driver.Value{{r.fp, r.done, r.result}}}, nil
}

type fakeRows struct {
	vals [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return []string{"fingerprint", "done", "result"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.vals) {
		return io.EOF
	}
	copy(dest, r.vals[r.i])
	r.i++
	return nil
}

func newFakeSQL(t *testing.T) DedupeStore {
	t.Helper()
	db, err := sql.Open("kritrimfake", t.Name()+string(rune('a'+fakeCount.Add(1))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := SQLDedupe(db, SQLConfig{Dialect: "sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// testDedupe is the behaviour every DedupeStore must have.
func testDedupe(t *testing.T, s DedupeStore) {
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)
	c, err := s.Claim(ctx, "a", "fp1", exp)
	if err != nil || !c.Won {
		t.Fatalf("first claim: %+v %v", c, err)
	}
	if c, err := s.Claim(ctx, "a", "fp1", exp); err != nil || c.Won || c.Done {
		t.Fatalf("in-flight claim: %+v %v", c, err)
	}
	if _, err := s.Claim(ctx, "a", "fp2", exp); !errors.Is(err, ErrConflict) {
		t.Fatalf("different payload: %v", err)
	}
	if err := s.Complete(ctx, "a", []byte(`{"text":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Claim(ctx, "a", "fp1", exp); err != nil || !c.Done || string(c.Result) != `{"text":"hi"}` {
		t.Fatalf("done claim: %+v %v", c, err)
	}
	// A released delivery can be won again.
	if c, _ := s.Claim(ctx, "b", "fp", exp); !c.Won {
		t.Fatal("claim b")
	}
	if err := s.Release(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Claim(ctx, "b", "fp", exp); !c.Won {
		t.Fatal("released delivery not claimable")
	}
	// An expired, finished delivery is forgotten.
	past := time.Now().Add(-time.Second)
	if c, _ := s.Claim(ctx, "c", "fp", past); !c.Won {
		t.Fatal("claim c")
	}
	_ = s.Complete(ctx, "c", []byte("{}"))
	if c, _ := s.Claim(ctx, "c", "fp", exp); !c.Won {
		t.Fatal("expired delivery still remembered")
	}
	// Long ids work.
	long := strings.Repeat("k", 500)
	if c, err := s.Claim(ctx, long, "fp", exp); err != nil || !c.Won {
		t.Fatalf("long id: %+v %v", c, err)
	}
	if c, _ := s.Claim(ctx, long, "fp", exp); c.Won {
		t.Fatal("long id claimed twice")
	}
}

func TestMemoryDedupe(t *testing.T) { testDedupe(t, MemoryDedupe(0)) }
func TestSQLDedupe(t *testing.T)    { testDedupe(t, newFakeSQL(t)) }

func TestSQLDedupeConfig(t *testing.T) {
	db, _ := sql.Open("kritrimfake", "config")
	defer db.Close()
	if _, err := SQLDedupe(db, SQLConfig{Table: "x; drop"}); err == nil {
		t.Fatal("bad table accepted")
	}
	if _, err := SQLDedupe(db, SQLConfig{Dialect: "oracle"}); err == nil {
		t.Fatal("unknown dialect accepted")
	}
	pg := &sqlDedupe{cfg: SQLConfig{Dialect: "postgres"}}
	if got := pg.q("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("postgres placeholders: %q", got)
	}
}

func TestWebhookSharedStoreAcrossInstances(t *testing.T) {
	store := newFakeSQL(t)
	h1, m1 := newHook(t, WebhookConfig{Store: store})
	h2, m2 := newHook(t, WebhookConfig{Store: store})
	now := time.Now()
	body := []byte(`{"prompt":"x"}`)
	sig := Sign("s3cret", now, "shared", body)
	if rr := deliver(h1, body, "shared", sig, now); rr.Code != 200 {
		t.Fatalf("first: %d", rr.Code)
	}
	rr := deliver(h2, body, "shared", sig, now)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"duplicate":true`) {
		t.Fatalf("second instance: %d %s", rr.Code, rr.Body.String())
	}
	if m1.n.Load()+m2.n.Load() != 1 {
		t.Fatalf("runs %d+%d, want 1", m1.n.Load(), m2.n.Load())
	}
}

func TestSchedulersShareTicks(t *testing.T) {
	store := MemoryDedupe(0)
	var models [3]*counterModel
	var scheds []*Scheduler
	for i := range models {
		models[i] = &counterModel{}
		a, err := kritrim.New(kritrim.Config{Model: models[i]})
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		s, err := NewScheduler(a, Schedule{Name: "report", Every: 50 * time.Millisecond, Prompt: "go"})
		if err != nil {
			t.Fatal(err)
		}
		s.SetStore(store)
		scheds = append(scheds, s)
	}
	for _, s := range scheds {
		s.Start(context.Background())
	}
	time.Sleep(330 * time.Millisecond)
	for _, s := range scheds {
		s.Close()
	}
	total := 0
	for _, m := range models {
		total += int(m.n.Load())
	}
	// About six ticks happened; three schedulers without a store would
	// have run each of them three times.
	if total < 3 || total > 8 {
		t.Fatalf("runs %d, want one per tick (about 6)", total)
	}
}

func TestSchedulerValidation(t *testing.T) {
	a, _ := kritrim.New(kritrim.Config{Model: kritrim.Mock()})
	defer a.Close()
	bad := [][]Schedule{
		{{Name: "x"}},
		{{Name: "x", Every: time.Second, Cron: "@daily"}},
		{{Name: "x", Cron: "bogus"}},
		{{Name: "x", Every: time.Second}, {Name: "x", Every: time.Minute}},
	}
	for i, s := range bad {
		if _, err := NewScheduler(a, s...); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	if _, err := NewScheduler(a, Schedule{Cron: "@daily", Thread: "t"}, Schedule{Cron: "0 9 * * mon-fri"}); err != nil {
		t.Fatal(err)
	}
}

// A claim whose leader never finished is taken over once it expires, as the
// SQL store does. Before, the memory store kept it forever.
func TestMemoryDedupeTakesOverExpiredClaim(t *testing.T) {
	ctx := context.Background()
	s := MemoryDedupe(10)
	past := time.Now().Add(-time.Second)
	if c, err := s.Claim(ctx, "d1", "p", past); err != nil || !c.Won {
		t.Fatalf("first claim: %+v %v", c, err)
	}
	// The leader's claim has expired and was never completed.
	c, err := s.Claim(ctx, "d1", "p", time.Now().Add(time.Hour))
	if err != nil || !c.Won {
		t.Fatalf("expired unfinished claim was not taken over: %+v %v", c, err)
	}
}

// A leader whose claim expired must not release the claim a newer leader
// now holds. Before the fence, its deferred Release deleted it.
func TestStaleLeaderCannotReleaseNewerClaim(t *testing.T) {
	ctx := context.Background()
	s := MemoryDedupe(10)
	oldExp := time.Now().Add(-time.Second)
	if c, _ := s.Claim(ctx, "d", "p", oldExp); !c.Won {
		t.Fatal("first claim not won")
	}
	newExp := time.Now().Add(time.Hour)
	if c, _ := s.Claim(ctx, "d", "p", newExp); !c.Won {
		t.Fatal("takeover not won")
	}
	// The old leader finishes late and releases with its own fence.
	_ = s.Release(withFence(ctx, oldExp), "d")
	if c, _ := s.Claim(ctx, "d", "p", time.Now().Add(time.Hour)); c.Won {
		t.Fatal("a stale leader's release freed the live claim")
	}
}
