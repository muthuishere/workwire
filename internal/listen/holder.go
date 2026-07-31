package listen

import (
	"os"
	"path/filepath"
	"strings"
)

// The flock is keyed by agent NAME, and it must stay that way: credentials,
// the session inbox, the cursor and the hub lease are all name-scoped, so
// hashing a folder into the key would give two same-named listeners one
// credential, one inbox and a deadlock on one lease.
//
// What the lock cannot say is WHICH folder is holding it — and a derived name
// is derived from the tree, so two checkouts of one repo+branch land on the
// same lock.
// The holder file says it: a plain, readable sidecar next to the lock (the
// lock file itself is unreadable to other processes on Windows, where the
// exclusive open IS the lock).

func holderPath(runDir, agent string) string {
	return filepath.Join(runDir, agent+".owner")
}

// WriteHolder records the absolute directory of the process holding the lock
// for this name. Best effort: a missing holder file only means "unknown".
func WriteHolder(runDir, agent, dir string) error {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(holderPath(runDir, agent), []byte(dir+"\n"), 0o644)
}

// HolderDir reports the directory recorded for this name, or "" when nothing
// recorded one (an older listener, or a lock taken by something else).
func HolderDir(runDir, agent string) string {
	b, err := os.ReadFile(holderPath(runDir, agent))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ClearHolder removes the record on graceful shutdown.
func ClearHolder(runDir, agent string) {
	_ = os.Remove(holderPath(runDir, agent))
}
