package kritrim

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrSessionTampered means a stored session did not match its signature,
// so the agent refused to replay it.
var ErrSessionTampered = errors.New("kritrim: session failed its signature check")

// SignedSessions wraps a store so every conversation it hands back has been
// proved to be the one the agent wrote.
//
//	store := kritrim.SignedSessions(kritrim.FileSessions(".agent/sessions"), key)
//
// Why this exists: message history is an instruction channel. An agent
// replays whatever the store returns and acts on it, so anyone who can
// write to the store — a shared Redis, a database with a wide grant, a
// mounted volume, a backup restored from somewhere else — can put words in
// the user's mouth or in a tool result and steer the next run. Signing
// makes the store hold data the agent verifies instead of data it trusts.
//
// Save computes an HMAC-SHA256 over the thread id, the messages, the
// session values and the pending/paused approval state. Load recomputes it
// and returns ErrSessionTampered on any mismatch, and on any session that
// carries no signature at all — an attacker who can edit the record can
// also delete the signature, so an unsigned record cannot be accepted.
//
// What it does not do: the signature covers content, not freshness. It
// does not stop someone with write access from deleting a session or
// restoring an older signed copy of it (rollback). Session.Version and its
// compare-and-swap guard concurrent writers, not a determined one. Keep
// the key out of the store it protects; a reader who also has the key can
// forge a record.
//
// Migrating a store that already has unsigned sessions: read each one
// through the unwrapped store and Save it through the wrapped one once.
func SignedSessions(store SessionStore, key []byte) SessionStore {
	return &signedSessions{store: store, key: append([]byte(nil), key...)}
}

type signedSessions struct {
	store SessionStore
	key   []byte
}

func (s *signedSessions) Load(ctx context.Context, id string) (*Session, error) {
	if len(s.key) == 0 {
		return nil, errors.New("kritrim: SignedSessions needs a key")
	}
	v, err := s.store.Load(ctx, id)
	if err != nil || v == nil {
		return v, err
	}
	if v.Sig == "" {
		return nil, fmt.Errorf("%w: session %s carries no signature", ErrSessionTampered, idOf(id))
	}
	want, err := s.sign(v)
	if err != nil {
		return nil, err
	}
	// Constant time: a timing oracle here would let a writer search for a
	// signature one byte at a time.
	if !hmac.Equal([]byte(v.Sig), []byte(want)) {
		return nil, fmt.Errorf("%w: session %s", ErrSessionTampered, idOf(id))
	}
	return v, nil
}

func (s *signedSessions) Save(ctx context.Context, v *Session) error {
	if len(s.key) == 0 {
		return errors.New("kritrim: SignedSessions needs a key")
	}
	sig, err := s.sign(v)
	if err != nil {
		return err
	}
	v.Sig = sig
	return s.store.Save(ctx, v)
}

// signedPayload is what the signature covers. Version and Updated are
// excluded: the underlying store assigns them inside Save, after the
// signature is computed.
type signedPayload struct {
	ID       string         `json:"id"`
	Messages []Message      `json:"messages"`
	Values   map[string]any `json:"values,omitempty"`
	Pending  []Interrupt    `json:"pending,omitempty"`
	Paused   *PausedStep    `json:"paused,omitempty"`
}

func (s *signedSessions) sign(v *Session) (string, error) {
	// encoding/json sorts map keys, so the payload is canonical.
	b, err := json.Marshal(signedPayload{ID: idOf(v.ID), Messages: v.Messages,
		Values: v.Values, Pending: v.Pending, Paused: v.Paused})
	if err != nil {
		return "", fmt.Errorf("kritrim: sign session: %w", err)
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil)), nil
}
