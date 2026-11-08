package kritrim

import (
	"bufio"
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

// AuditSink receives audit entries. AuditFile is the usual sink.
type AuditSink interface {
	Write(ctx context.Context, e Entry) error
}

// Entry is one audit record. Hash chains every entry to the one before it.
type Entry struct {
	Seq    int    `json:"seq"`
	Time   int64  `json:"time"`
	Kind   string `json:"kind"` // model | tool | approval | run
	Name   string `json:"name,omitempty"`
	Detail string `json:"detail,omitempty"`
	Run    string `json:"run,omitempty"`
	Thread string `json:"thread,omitempty"`
	User   string `json:"user,omitempty"`
	Prev   string `json:"prev"`
	Hash   string `json:"hash"`
}

// AuditFile returns a sink that appends hash-chained JSON lines to path.
// VerifyAudit checks the chain later.
func AuditFile(path string) AuditSink { return &fileAudit{path: path} }

type fileAudit struct {
	mu   sync.Mutex
	path string
	prev string
	seq  int
}

func (a *fileAudit) Write(ctx context.Context, e Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if a.seq == 0 && a.prev == "" {
		last, err := lastEntry(a.path)
		if err != nil {
			return fmt.Errorf("kritrim: audit log %s: %w", a.path, err)
		}
		if last != nil {
			a.seq, a.prev = last.Seq, last.Hash
		}
	}
	a.seq++
	e.Seq = a.seq
	if e.Time == 0 {
		e.Time = time.Now().UnixMilli()
	}
	e.Prev = a.prev
	e.Hash = hashEntry(e)
	a.prev = e.Hash
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

func hashEntry(e Entry) string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(append([]byte(e.Prev), b...))
	return hex.EncodeToString(sum[:])
}

func lastEntry(path string) (*Entry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var last *Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<22)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, err
		}
		last = &e
	}
	return last, sc.Err()
}

// VerifyAudit checks that every entry's hash and prev link are intact.
func VerifyAudit(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<22)
	prev, n := "", 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return fmt.Errorf("kritrim: audit line %d: %w", n+1, err)
		}
		if e.Prev != prev {
			return fmt.Errorf("kritrim: audit line %d: prev does not match", n+1)
		}
		if hashEntry(e) != e.Hash {
			return fmt.Errorf("kritrim: audit line %d: hash does not match", n+1)
		}
		prev = e.Hash
		n++
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if n == 0 {
		return errors.New("kritrim: audit log is empty")
	}
	return nil
}

// AuditFunc adapts a function to AuditSink.
type AuditFunc func(ctx context.Context, e Entry) error

func (f AuditFunc) Write(ctx context.Context, e Entry) error { return f(ctx, e) }
