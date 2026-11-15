//go:build unix

package kritrim

import (
	"os"
	"syscall"
)

// lockLog takes an exclusive advisory lock on the audit log. The returned
// func releases it.
func lockLog(f *os.File) (func(), error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
}
