//go:build !unix

package kritrim

import "os"

// lockFile is a no-op where flock is unavailable. Run one writer per log or
// session store on those platforms.
func lockFile(f *os.File) (func(), error) { return func() {}, nil }
