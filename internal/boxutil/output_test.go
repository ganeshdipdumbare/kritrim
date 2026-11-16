package boxutil

import (
	"bytes"
	"strings"
	"testing"
)

func TestCappedDropsPastTheLimit(t *testing.T) {
	c := &Capped{Max: 4}
	c.Write([]byte("abcdef"))
	got := c.String()
	if !strings.HasPrefix(got, "abcd") || !strings.Contains(got, "truncated") {
		t.Fatalf("got %q", got)
	}
}

func TestCappedUnderLimitIsExact(t *testing.T) {
	c := &Capped{Max: 10}
	c.Write([]byte("hello"))
	if c.String() != "hello" {
		t.Fatalf("got %q", c.String())
	}
}

// A line far longer than bufio.Scanner's 64 KiB token limit must come through whole.
// Lines frame output: one chunk per line, the newline kept, nothing added.
func TestLinesFrameOutput(t *testing.T) {
	var dst = Capped{Max: 100}
	var got []string
	if err := Lines(strings.NewReader("one\ntwo"), &dst, func(s string) { got = append(got, s) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "one\n" || got[1] != "two" || dst.String() != "one\ntwo" {
		t.Fatalf("chunks %q kept %q", got, dst.String())
	}
}

func TestLinesKeepsLongLines(t *testing.T) {
	long := strings.Repeat("x", 200<<10)
	var dst Capped = Capped{Max: 1 << 20}
	var emitted bytes.Buffer
	if err := Lines(strings.NewReader(long+"\n"), &dst, func(s string) { emitted.WriteString(s) }); err != nil {
		t.Fatal(err)
	}
	if dst.String() != long+"\n" || emitted.String() != long+"\n" {
		t.Fatalf("long line was cut: kept %d bytes", len(dst.String()))
	}
}
