//go:build unix

// The local singleton fast path: an OS-held advisory lock on an open fd
// under the config/run dir — NOT a pid lockfile. The lock dies with the
// process, so it is correct across kill -9 and container restarts
// (ADR-003, agent-skill R4).
package listen

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock is a held flock; Release closes the fd (which drops the lock).
type Lock struct {
	f *os.File
}

// ErrLocked reports another live listener on this host.
type ErrLocked struct{ Path string }

func (e ErrLocked) Error() string {
	return fmt.Sprintf("another workwire listen already holds %s on this host", e.Path)
}

// AcquireLock takes an exclusive non-blocking flock on
// <runDir>/<agent>.lock. A stale file from a dead process never blocks
// acquisition — the kernel released its lock when the process died.
func AcquireLock(runDir, agent string) (*Lock, error) {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(runDir, agent+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, ErrLocked{Path: path}
		}
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. Safe to call once; the lock also dies with the
// process without it.
func (l *Lock) Release() {
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}
