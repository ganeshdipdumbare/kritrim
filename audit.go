package kritrim

import (
	"bufio"
	"context"
	"crypto/hmac"
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
	// Source is "box" for entries written by a sealed box.
	Source string `json:"source,omitempty"`
	Prev   string `json:"prev"`
	Hash   string `json:"hash"`
}

// AuditOption configures AuditFile and VerifyAudit.
type AuditOption func(*auditOptions)

type auditOptions struct{ key []byte }

// AuditKey makes the log tamper-evident against someone who can write the
// file: every entry is chained with HMAC-SHA256 under key, and a signed head
// file (path + ".head") records the last entry so truncation is detected.
// Use at least 32 random bytes kept outside the log's host (a KMS or secret
// store). Without a key the chain is plain SHA-256: it catches accidental
// corruption but anyone with write access can recompute it.
func AuditKey(key []byte) AuditOption {
	return func(o *auditOptions) { o.key = append([]byte(nil), key...) }
}

// AuditFile returns a sink that appends hash-chained JSON lines to path.
// VerifyAudit checks the chain later.
func AuditFile(path string, opts ...AuditOption) AuditSink {
	var o auditOptions
	for _, fn := range opts {
		fn(&o)
	}
	return &fileAudit{path: path, key: o.key}
}

// errAuditKey is returned when a key is set but too short to be safe.
var errAuditKey = errors.New("kritrim: audit key must be at least 32 bytes")

type fileAudit struct {
	mu   sync.Mutex
	path string
	key  []byte
	prev string
	seq  int
}

func (a *fileAudit) Write(ctx context.Context, e Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.key != nil && len(a.key) < 32 {
		return errAuditKey
	}
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
	e.Hash = hashEntry(e, a.key)
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		a.seq--
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	a.prev = e.Hash
	if a.key != nil {
		return writeHead(a.path, a.key, e.Seq, e.Hash)
	}
	return nil
}

func hashEntry(e Entry, key []byte) string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	if key != nil {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(e.Prev))
		m.Write(b)
		return hex.EncodeToString(m.Sum(nil))
	}
	sum := sha256.Sum256(append([]byte(e.Prev), b...))
	return hex.EncodeToString(sum[:])
}

// auditHead is the signed record of the last entry, kept next to the log.
type auditHead struct {
	Seq  int    `json:"seq"`
	Hash string `json:"hash"`
	MAC  string `json:"mac"`
}

func headMAC(key []byte, seq int, hash string) string {
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "kritrim-audit-head\x00%d\x00%s", seq, hash)
	return hex.EncodeToString(m.Sum(nil))
}

func writeHead(path string, key []byte, seq int, hash string) error {
	b, _ := json.Marshal(auditHead{Seq: seq, Hash: hash, MAC: headMAC(key, seq, hash)})
	tmp := path + ".head.tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path+".head")
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

// VerifyAudit checks that every entry's hash and prev link are intact. With
// AuditKey it also checks every HMAC and the signed head, so a rewritten or
// truncated log fails.
func VerifyAudit(path string, opts ...AuditOption) error {
	var o auditOptions
	for _, fn := range opts {
		fn(&o)
	}
	if o.key != nil && len(o.key) < 32 {
		return errAuditKey
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<22)
	prev, n := "", 0
	hashes := map[int]string{}
	var last Entry
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
		if !hmac.Equal([]byte(hashEntry(e, o.key)), []byte(e.Hash)) {
			return fmt.Errorf("kritrim: audit line %d: hash does not match", n+1)
		}
		if e.Seq != n+1 {
			return fmt.Errorf("kritrim: audit line %d: sequence is %d", n+1, e.Seq)
		}
		prev = e.Hash
		hashes[e.Seq] = e.Hash
		last = e
		n++
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if n == 0 {
		return errors.New("kritrim: audit log is empty")
	}
	if o.key == nil {
		return nil
	}
	b, err := os.ReadFile(path + ".head")
	if err != nil {
		return fmt.Errorf("kritrim: audit head: %w", err)
	}
	var h auditHead
	if err := json.Unmarshal(b, &h); err != nil {
		return fmt.Errorf("kritrim: audit head: %w", err)
	}
	if !hmac.Equal([]byte(headMAC(o.key, h.Seq, h.Hash)), []byte(h.MAC)) {
		return errors.New("kritrim: audit head signature does not match")
	}
	// The head may lag the log by one entry if the process stopped between
	// the append and the head update; anything else is truncation.
	if hashes[h.Seq] != h.Hash || (last.Seq != h.Seq && last.Seq != h.Seq+1) {
		return fmt.Errorf("kritrim: audit log ends at %d but the head records %d (truncated or rewritten)", last.Seq, h.Seq)
	}
	return nil
}

// AuditFunc adapts a function to AuditSink.
type AuditFunc func(ctx context.Context, e Entry) error

func (f AuditFunc) Write(ctx context.Context, e Entry) error { return f(ctx, e) }
