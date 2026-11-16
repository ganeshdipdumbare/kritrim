//go:build !unix

package kritrim

import (
	"os"
	"sync"
)

// lockFile serializes goroutines in this process that lock the same path.
// Where flock is unavailable, that is all it can do: processes sharing one
// log or session store must not write it at the same time on these platforms.
func lockFile(f *os.File) (func(), error) {
	key := f.Name()
	localLocks.Lock()
	mu := localLocks.m[key]
	if mu == nil {
		mu = &sync.Mutex{}
		localLocks.m[key] = mu
	}
	localLocks.Unlock()
	mu.Lock()
	return mu.Unlock, nil
}

var localLocks = struct {
	sync.Mutex
	m map[string]*sync.Mutex
}{m: map[string]*sync.Mutex{}}
