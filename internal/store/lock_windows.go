//go:build windows

package store

import (
	"fmt"
	"syscall"
)

// dirLock is an OS-held exclusive lock on the data dir. On Windows there is no
// flock, but opening the lock file with a share mode of ZERO is the same
// bargain: the kernel refuses any second open of the same path until this
// handle is closed, and it closes the handle when the process dies — so the
// lock is never stale after a kill or a container redeploy (hub-core R12).
type dirLock struct {
	h syscall.Handle
}

func acquireLock(path string) (*dirLock, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(
		p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, // no sharing: this is the lock
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("lock file %s: %w", path, err)
	}
	return &dirLock{h: h}, nil
}

func (l *dirLock) release() {
	if l.h != syscall.InvalidHandle && l.h != 0 {
		syscall.CloseHandle(l.h)
		l.h = syscall.InvalidHandle
	}
}
