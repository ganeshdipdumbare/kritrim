package boxutil

import (
	"io"
	"os"
)

// SafeStderr is the host's stderr for output a sandbox or server produced.
// That output is untrusted: control characters are replaced, so it cannot
// move the cursor, clear the screen or rewrite the terminal title. Newlines
// and tabs pass through.
func SafeStderr() io.Writer { return safeWriter{w: os.Stderr} }

type safeWriter struct{ w io.Writer }

func (s safeWriter) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for _, c := range p {
		if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
			c = '?'
		}
		out = append(out, c)
	}
	if _, err := s.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
