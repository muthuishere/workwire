package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/listen"
)

// A derived agent name is `filepath.Base(dir)`, so `~/src/api` and
// `~/work/other/api` both want to be `api`. Nothing checked, so the second
// folder was told it had joined while it was on the wire under no name at all
// — and `ask api "..."` was then answered confidently about a different
// codebase.
//
// folders.json is the local binding that makes a folder's identity stable and
// a collision detectable: abs dir -> name. It is deliberately NOT a rename
// scheme — the name stays the folder's own basename, and the flock stays
// name-keyed (everything downstream is). What changes is that a second folder
// wanting a taken name is TOLD, with the hub's own suggestion, instead of
// silently sharing an identity.

const foldersFileName = "folders.json"

type folderBinding struct {
	Name string `json:"name"`
}

type foldersFile struct {
	Folders map[string]folderBinding `json:"folders"`
}

func foldersPath(cfg config.Config) string {
	return filepath.Join(cfg.ConfigDir, foldersFileName)
}

// loadFolders reads the bindings. A missing or corrupt file is empty: this is
// a cache of local facts, never a reason to fail a session start.
func loadFolders(cfg config.Config) foldersFile {
	ff := foldersFile{Folders: map[string]folderBinding{}}
	if cfg.ConfigDir == "" {
		return ff
	}
	b, err := os.ReadFile(foldersPath(cfg))
	if err != nil {
		return ff
	}
	if err := json.Unmarshal(b, &ff); err != nil || ff.Folders == nil {
		return foldersFile{Folders: map[string]folderBinding{}}
	}
	return ff
}

func saveFolderBinding(cfg config.Config, dir, name string) {
	if cfg.ConfigDir == "" {
		return
	}
	ff := loadFolders(cfg)
	ff.Folders[absOf(dir)] = folderBinding{Name: name}
	b, err := json.MarshalIndent(ff, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(cfg.ConfigDir, 0o755); err != nil {
		return
	}
	tmp := foldersPath(cfg) + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o644) == nil {
		_ = os.Rename(tmp, foldersPath(cfg))
	}
}

// boundName is the name this folder joined under before, if any.
func boundName(cfg config.Config, dir string) string {
	return loadFolders(cfg).Folders[absOf(dir)].Name
}

// folderHoldingName returns the folder already bound to this name, "" if none.
func folderHoldingName(cfg config.Config, name string) string {
	for d, b := range loadFolders(cfg).Folders {
		if b.Name == name {
			return d
		}
	}
	return ""
}

// absOf is the canonical form of a folder path: absolute AND symlink-resolved,
// so `--dir /tmp/x` and the cwd the same session reports (`/private/tmp/x` on
// macOS) are recognised as one folder rather than two colliding ones.
func absOf(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// nameConflict reports the OTHER folder already using this name, or "" when
// the name is this folder's to use. Two sources, both local: the persisted
// binding (survives restarts) and the live lock holder (covers a first run).
func nameConflict(cfg config.Config, name, dir string) string {
	dir = absOf(dir)
	if owner := folderHoldingName(cfg, name); owner != "" && owner != dir {
		return owner
	}
	if cfg.ConfigDir != "" {
		if holder := listen.HolderDir(filepath.Join(cfg.ConfigDir, "run"), name); holder != "" && absOf(holder) != dir {
			return holder
		}
	}
	return ""
}

// suggestFreeName asks the hub for the name it would offer — the hub already
// owns the `<name>-N` suggestion for a taken name (registry-a2a R2), so the
// CLI does not invent a second scheme. It probes the card first so a name the
// hub does not know is never created as a side effect of asking. When the hub
// cannot answer, fall back to a local disambiguation from the parent folder
// (`other/api` -> `other-api`), which is the more useful name anyway.
func suggestFreeName(cfg config.Config, name, dir string) string {
	if s := hubSuggestion(cfg, name); s != "" {
		return s
	}
	parent := filepath.Base(filepath.Dir(absOf(dir)))
	if parent != "" && parent != "." && parent != string(filepath.Separator) && parent != name {
		return parent + "-" + name
	}
	return ""
}

func hubSuggestion(cfg config.Config, name string) string {
	c := newClient(cfg)
	// Only ask about a name the hub already holds: a POST for an unknown name
	// would REGISTER it, and asking a question must not create a peer.
	if code, err := c.do("GET", "/agents/"+url.PathEscape(name)+"/card", nil, nil); err != nil || code != 200 {
		return ""
	}
	var out struct {
		Suggestion string `json:"suggestion"`
	}
	// No credential for this name is presented, so the hub answers 409 with
	// its suggestion and leaves the existing registration untouched.
	code, err := c.do("POST", "/agents", map[string]any{"name": name}, &out)
	if err != nil || code != 409 {
		return ""
	}
	return out.Suggestion
}

// conflictMessage is what a human (or a model) reads when two folders want
// one name. It names both folders and what to do about it.
func conflictMessage(name, other, dir, suggestion string) string {
	msg := fmt.Sprintf("workwire: %q is already the peer name for %s — this folder (%s) has NOT joined", name, other, dir)
	if suggestion != "" {
		msg += fmt.Sprintf("\nworkwire: join it under a distinct name instead: workwire listen --agent %s --dir %s", suggestion, dir)
	} else {
		msg += fmt.Sprintf("\nworkwire: join it under a distinct name instead: workwire listen --agent <name> --dir %s", dir)
	}
	return msg
}
