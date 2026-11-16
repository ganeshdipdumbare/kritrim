//go:build unix

package kritrim

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on f. The lock is held per open
// file, so it also excludes other goroutines that open the file. The returned
// func releases it.
func lockFile(f *os.File) (func(), error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
}
