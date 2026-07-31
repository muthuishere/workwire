// Package origin derives a peer's provenance — which tree is talking
// (ADR-011 §1). Detection is client-side and best-effort: a non-git
// directory, a broken repo or a missing git binary all yield empty fields,
// never an error. The hub stores and serves it and never verifies it.
package origin

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Info is the provenance block sent at registration and refreshed on
// heartbeat, because people switch branches mid-session.
type Info struct {
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
	Cwd    string `json:"cwd,omitempty"`
	Host   string `json:"host,omitempty"`
}

// Empty reports whether there is nothing worth rendering.
func (i *Info) Empty() bool {
	return i == nil || (i.Repo == "" && i.Branch == "" && i.Commit == "")
}

// String renders provenance the way peers, threads and context show it:
// `owner/name@branch abc1234`, with a trailing `*` when the tree is dirty.
func (i *Info) String() string {
	if i.Empty() {
		return ""
	}
	s := i.Repo
	if i.Branch != "" {
		s += "@" + i.Branch
	}
	if i.Commit != "" {
		s += " " + i.Commit
	}
	if i.Dirty {
		s += "*"
	}
	return strings.TrimSpace(s)
}

// FromMap rebuilds an Info from a decoded JSON object (envelope meta).
func FromMap(m map[string]any) *Info {
	if m == nil {
		return nil
	}
	str := func(k string) string {
		s, _ := m[k].(string)
		return s
	}
	dirty, _ := m["dirty"].(bool)
	i := &Info{Repo: str("repo"), Branch: str("branch"), Commit: str("commit"), Dirty: dirty, Cwd: str("cwd"), Host: str("host")}
	if i.Empty() && i.Cwd == "" && i.Host == "" {
		return nil
	}
	return i
}

// Map renders Info as a plain JSON-able map for envelope meta.
func (i *Info) Map() map[string]any {
	if i == nil {
		return nil
	}
	m := map[string]any{}
	if i.Repo != "" {
		m["repo"] = i.Repo
	}
	if i.Branch != "" {
		m["branch"] = i.Branch
	}
	if i.Commit != "" {
		m["commit"] = i.Commit
	}
	if i.Dirty {
		m["dirty"] = true
	}
	if i.Cwd != "" {
		m["cwd"] = i.Cwd
	}
	if i.Host != "" {
		m["host"] = i.Host
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// git runs one git command in dir; any failure is an empty string.
func git(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// NormalizeRepo reduces a git remote URL to `owner/name`.
func NormalizeRepo(remote string) string {
	s := strings.TrimSpace(remote)
	if s == "" {
		return ""
	}
	s = strings.TrimSuffix(s, ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		// scheme://host/owner/name — drop scheme and host.
		s = s[i+3:]
		if j := strings.Index(s, "/"); j >= 0 {
			s = s[j+1:]
		}
	} else if i := strings.LastIndex(s, ":"); i >= 0 {
		// scp-style git@host:owner/name — drop everything up to the colon.
		s = s[i+1:]
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return strings.Join(parts, "/")
}

// Detect derives provenance for dir (cwd when empty). It never fails.
func Detect(dir string) *Info {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	host, _ := os.Hostname()
	i := &Info{Cwd: dir, Host: host}
	if git(dir, "rev-parse", "--is-inside-work-tree") != "true" {
		return i // not a git tree (or no git at all): no repo/branch, no error
	}
	if remote := git(dir, "remote", "get-url", "origin"); remote != "" {
		i.Repo = NormalizeRepo(remote)
	} else if top := git(dir, "rev-parse", "--show-toplevel"); top != "" {
		i.Repo = baseName(top)
	}
	i.Branch = git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if i.Branch == "" {
		// An unborn branch (fresh `git init`, no commit yet) has no HEAD to
		// resolve, but it does have a name — and a peer joining from a brand
		// new repo should still be `<repo>-<branch>`, not just the folder.
		i.Branch = git(dir, "symbolic-ref", "--short", "HEAD")
	}
	i.Commit = git(dir, "rev-parse", "--short", "HEAD")
	i.Dirty = git(dir, "status", "--porcelain") != ""
	return i
}

// DeriveName is the default peer name: `<repo>-<branch>`, e.g. `workwire-main`
// or `toolnexus-docs-api-sections-wave4`.
//
// The folder basename alone was wrong, and wrong in the way that matters: two
// sessions on two branches of the SAME repo are two different codebases with
// two different answers, and under a folder-derived name they collided into
// one peer (or, worse, one of them was told a name was taken and went quiet).
// A worktree of `cljgo` on `feat/x` is not `cljgo`. Branch is part of who you
// are, so it is part of the name — `main` included, because a scheme that only
// sometimes appends the branch is one nobody can predict.
//
// Outside a git tree there is no branch to add, so the folder basename stands.
// A detached HEAD names the commit instead — it is what identifies that tree.
func DeriveName(dir string) string {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	folder := sanitizeName(baseName(strings.TrimRight(dir, "/")))
	i := Detect(dir)
	if i.Repo == "" && i.Branch == "" {
		return folder // not a git tree: the folder is the whole identity
	}
	repo := i.Repo
	if j := strings.LastIndex(repo, "/"); j >= 0 {
		repo = repo[j+1:] // owner/name -> name
	}
	if repo == "" {
		repo = folder
	}
	branch := i.Branch
	if branch == "HEAD" || branch == "" { // detached HEAD: the commit identifies it
		branch = i.Commit
	}
	if branch == "" {
		return folder
	}
	return sanitizeName(repo + "-" + branch)
}

// sanitizeName keeps a derived name safe for a URL path element, a file name
// and a flock: letters, digits, `.`, `_` and `-`, with runs of separators
// collapsed. It never returns an empty string for non-empty input.
func sanitizeName(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return strings.Trim(s, "-")
	}
	return out
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Describe is a short one-line rendering used in CLI errors and logs.
func Describe(i *Info) string {
	if s := i.String(); s != "" {
		return s
	}
	if i != nil && i.Cwd != "" {
		return fmt.Sprintf("(no repo) %s", i.Cwd)
	}
	return "(no provenance)"
}
