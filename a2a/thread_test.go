package a2a

import "testing"

// Principals whose IDs contain the separator must not share a thread with
// another principal's conversation.
func TestThreadIDsDoNotCollide(t *testing.T) {
	a := threadID("a:b", "c")
	b := threadID("a", "b:c")
	if a == b {
		t.Fatalf("threads collide: %q", a)
	}
}
