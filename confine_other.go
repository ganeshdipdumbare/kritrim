//go:build !(linux && (amd64 || arm64))

package kritrim

import (
	"fmt"
	"runtime"
)

// confineSupported fails closed: Confined has no enforcement here.
func confineSupported(bool) error {
	return fmt.Errorf("kritrim: Confined needs Linux on amd64 or arm64 (Landlock and seccomp); this is %s/%s", runtime.GOOS, runtime.GOARCH)
}
