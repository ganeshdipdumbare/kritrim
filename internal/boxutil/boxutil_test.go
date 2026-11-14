package boxutil

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	got := Quote([]string{"echo", "it's", "a b", "", "x=1"})
	want := `echo 'it'\''s' 'a b' '' x=1`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestSSE(t *testing.T) {
	in := "event: x\ndata: {\"a\":1}\n\ndata: line1\ndata: line2\n\n: comment\ndata: last"
	var got []string
	if err := SSE(strings.NewReader(in), func(d []byte) bool { got = append(got, string(d)); return true }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != `{"a":1}` || got[1] != "line1\nline2" || got[2] != "last" {
		t.Fatalf("%q", got)
	}
}

func TestClientNoRedirectAndStatus(t *testing.T) {
	var sawKey bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("X-Key") != ""
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, other.URL, http.StatusFound)
			return
		}
		http.Error(w, "nope", http.StatusTeapot)
	}))
	defer srv.Close()
	c := NewClient("test", nil, map[string]string{"X-Key": "k"})
	_, err := c.Do(context.Background(), http.MethodGet, srv.URL+"/redirect", nil, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusFound || sawKey {
		t.Fatalf("redirect followed or wrong error: %v sawKey=%v", err, sawKey)
	}
	if err := c.JSON(context.Background(), http.MethodGet, srv.URL, nil, nil, nil); !errors.As(err, &se) || se.Code != http.StatusTeapot {
		t.Fatalf("status: %v", err)
	}
}

func TestProcess(t *testing.T) {
	var sent []string
	closed := false
	p := NewProcess(func(b []byte) error { sent = append(sent, string(b)); return nil }, func() error { closed = true; return nil }, nil)
	go func() {
		_ = p.Output([]byte("hello "))
		_ = p.Output([]byte("world"))
		p.Exit(&ExitError{Code: 3})
	}()
	if _, err := p.Stdin().Write([]byte("in")); err != nil {
		t.Fatal(err)
	}
	_ = p.Stdin().Close()
	out, _ := io.ReadAll(p.Stdout())
	err := p.Wait()
	var ee *ExitError
	if string(out) != "hello world" || !errors.As(err, &ee) || ee.Code != 3 || sent[0] != "in" || !closed {
		t.Fatalf("out=%q err=%v sent=%v closed=%v", out, err, sent, closed)
	}
}
