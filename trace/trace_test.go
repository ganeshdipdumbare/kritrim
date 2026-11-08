package trace

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ganeshdipdumbare/kritrim"
)

func TestJSONTrace(t *testing.T) {
	var buf bytes.Buffer
	a, err := kritrim.New(kritrim.Config{Model: kritrim.Mock(kritrim.Turn{Text: "ok"}), Hooks: JSON(&buf)})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Run(context.Background(), "hi", kritrim.Thread("t1")); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"run.start", "model.start", "model.end", "run.end"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
}
