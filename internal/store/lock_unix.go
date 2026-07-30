//go:build unix

package store

import (
	"fmt"
	"os"
	"syscall"
)

// dirLock is an OS-held advisory lock (flock) on the data dir. It dies with
// the process, so it is never stale after kill -9 or a container redeploy
// (hub-core R12).
type dirLock struct {
	f *os.File
}

func acquireLock(path string) (*dirLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("flock: %w", err)
	}
	return &dirLock{f: f}, nil
}

func (l *dirLock) release() {
	if l.f != nil {
		syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		l.f.Close()
		l.f = nil
	}
}
