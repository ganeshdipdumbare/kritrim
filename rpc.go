package kritrim

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// wire is one JSON-RPC message. A message with an ID is a request or its
// response; a message without an ID is a notification.
type wire struct {
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type handlerFunc func(context.Context, json.RawMessage) (any, error)

// peer is a bidirectional JSON-RPC connection over a byte stream.
type peer struct {
	enc     *json.Encoder
	writeMu sync.Mutex

	mu      sync.Mutex
	next    int64
	pending map[int64]chan wire

	hmu      sync.RWMutex
	handlers map[string]handlerFunc

	closed    chan struct{}
	closeOnce sync.Once
}

func newPeer(r io.Reader, w io.Writer) *peer {
	p := &peer{enc: json.NewEncoder(w), pending: map[int64]chan wire{},
		handlers: map[string]handlerFunc{}, closed: make(chan struct{})}
	go p.readLoop(r)
	return p
}

func (p *peer) handle(method string, fn handlerFunc) {
	p.hmu.Lock()
	p.handlers[method] = fn
	p.hmu.Unlock()
}

// maxFrame bounds one frame. Frames are newline-delimited (json.Encoder never
// writes a raw newline inside a value), so a longer line ends the peer. It
// matches the vmagent frame limit and is sized so that a full 8 MiB tool or
// HTTP result, which JSON escaping can grow several times, still fits.
const maxFrame = 64 << 20

// maxInFlight bounds guest-initiated handlers running at once. When the cap
// is reached, reading pauses until a handler finishes.
const maxInFlight = 64

func (p *peer) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxFrame)
	sem := make(chan struct{}, maxInFlight)
	for {
		if !sc.Scan() {
			p.shutdown()
			return
		}
		var m wire
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			p.shutdown()
			return
		}
		if m.Method == "" {
			p.mu.Lock()
			ch := p.pending[m.ID]
			delete(p.pending, m.ID)
			p.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		p.hmu.RLock()
		fn := p.handlers[m.Method]
		p.hmu.RUnlock()
		if fn == nil {
			if m.ID != 0 {
				p.reply(m.ID, nil, fmt.Errorf("unknown method %q", m.Method))
			}
			continue
		}
		sem <- struct{}{}
		go func(m wire, fn handlerFunc) {
			defer func() { <-sem }()
			res, err := fn(context.Background(), m.Params)
			if m.ID == 0 {
				return
			}
			p.reply(m.ID, res, err)
		}(m, fn)
	}
}

func (p *peer) reply(id int64, res any, err error) {
	if err != nil {
		p.send(wire{ID: id, Error: err.Error()})
		return
	}
	b, merr := json.Marshal(res)
	if merr != nil {
		p.send(wire{ID: id, Error: merr.Error()})
		return
	}
	p.send(wire{ID: id, Result: b})
}

func (p *peer) send(m wire) {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.enc.Encode(m)
}

func (p *peer) notify(method string, params any) {
	b, err := json.Marshal(params)
	if err != nil {
		return
	}
	p.send(wire{Method: method, Params: b})
}

// call sends a request and waits for its response.
func (p *peer) call(ctx context.Context, method string, params, out any) error {
	id := atomic.AddInt64(&p.next, 1)
	ch := make(chan wire, 1)
	p.mu.Lock()
	p.pending[id] = ch
	p.mu.Unlock()
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	p.send(wire{ID: id, Method: method, Params: b})
	select {
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return ctx.Err()
	case <-p.closed:
		return errors.New("kritrim: connection closed")
	case m := <-ch:
		if m.Error != "" {
			return errors.New(m.Error)
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	}
}

func (p *peer) shutdown() {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.mu.Lock()
		for _, ch := range p.pending {
			select {
			case ch <- wire{Error: "kritrim: connection closed"}:
			default:
			}
		}
		p.pending = map[int64]chan wire{}
		p.mu.Unlock()
	})
}

func (p *peer) Done() <-chan struct{} { return p.closed }
