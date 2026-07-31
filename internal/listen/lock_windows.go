//go:build windows

// The local singleton fast path on Windows. There is no flock, but a
// CreateFile with a share mode of ZERO gives the same guarantee: no second
// process can open the path, and the kernel closes the handle when the
// process dies — so the lock is correct across kill and restart, with no
// stale pid file (ADR-003, agent-skill R4).
package listen

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock is a held exclusive open; Release closes the handle (which drops it).
type Lock struct {
	h syscall.Handle
}

// ErrLocked reports another live listener on this host.
type ErrLocked struct{ Path string }

func (e ErrLocked) Error() string {
	return fmt.Sprintf("another workwire listen already holds %s on this host", e.Path)
}

// AcquireLock takes the exclusive handle on <runDir>/<agent>.lock.
func AcquireLock(runDir, agent string) (*Lock, error) {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(runDir, agent+".lock")
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
		return nil, ErrLocked{Path: path}
	}
	return &Lock{h: h}, nil
}

// Release drops the lock. Safe to call once; the lock also dies with the
// process without it.
func (l *Lock) Release() {
	if l.h != syscall.InvalidHandle && l.h != 0 {
		_ = syscall.CloseHandle(l.h)
		l.h = syscall.InvalidHandle
	}
}
