//go:build !unix

package kritrim

import "os"

// lockLog is a no-op where flock is unavailable. Run one writer per audit
// log on those platforms.
func lockLog(f *os.File) (func(), error) { return func() {}, nil }
