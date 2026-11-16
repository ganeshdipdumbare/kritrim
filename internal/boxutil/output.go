package boxutil

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// MaxOutput is how much of a command's stdout or stderr is kept. Output past
// it is dropped and the result says so. The sandbox is untrusted, so its
// output must not size the host's memory.
const MaxOutput = 8 << 20

// Capped keeps up to Max bytes written to it and drops the rest.
type Capped struct {
	Max  int
	buf  bytes.Buffer
	over bool
}

// NewCapped returns a Capped with the MaxOutput limit.
func NewCapped() *Capped { return &Capped{Max: MaxOutput} }

func (c *Capped) Write(p []byte) (int, error) {
	room := c.Max - c.buf.Len()
	if room <= 0 {
		c.over = c.over || len(p) > 0
		return len(p), nil
	}
	if len(p) > room {
		c.buf.Write(p[:room])
		c.over = true
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

// String returns what was kept, with a marker if anything was dropped.
func (c *Capped) String() string {
	if c.over {
		return c.buf.String() + "\n[output truncated]"
	}
	return c.buf.String()
}

// Lines copies rd into dst and hands each line to emit as it arrives, with
// its newline and without any trailing newline the output did not have. A
// line longer than the buffer (64 KiB) is passed on in pieces, so nothing is
// lost and memory stays bounded. A read error other than EOF is returned.
func Lines(rd io.Reader, dst *Capped, emit func(chunk string)) error {
	br := bufio.NewReaderSize(rd, 64<<10)
	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 {
			_, _ = dst.Write(line)
			if emit != nil {
				emit(string(line))
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Truncated reports whether output was dropped.
func (c *Capped) Truncated() bool { return c.over }

// Bytes returns the kept output without the truncation marker, for binary data.
func (c *Capped) Bytes() []byte { return c.buf.Bytes() }
