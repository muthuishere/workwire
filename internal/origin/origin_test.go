package origin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNormalizeRepo(t *testing.T) {
	cases := []struct{ in, want string }{
		{"git@github.com:muthuishere/workwire.git", "muthuishere/workwire"},
		{"https://github.com/muthuishere/workwire.git", "muthuishere/workwire"},
		{"https://gitlab.com/group/sub/name", "sub/name"},
		{"/srv/git/bare.git", "git/bare"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeRepo(c.in); got != c.want {
			t.Errorf("NormalizeRepo(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStringRendersProvenance(t *testing.T) {
	cases := []struct {
		name string
		in   *Info
		want string
	}{
		{"full", &Info{Repo: "muthuishere/workwire", Branch: "main", Commit: "a1b2c3d"}, "muthuishere/workwire@main a1b2c3d"},
		{"dirty marks the commit", &Info{Repo: "x/y", Branch: "feat/tokens", Commit: "f9e0d1", Dirty: true}, "x/y@feat/tokens f9e0d1*"},
		{"non-git is empty", &Info{Cwd: "/tmp", Host: "box"}, ""},
		{"nil is empty", nil, ""},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDetectNonGitDirIsCleanNotAnError(t *testing.T) {
	dir := t.TempDir()
	i := Detect(dir)
	if i == nil {
		t.Fatal("Detect must never return nil")
	}
	if i.Repo != "" || i.Branch != "" || i.Commit != "" {
		t.Fatalf("non-git dir reported a repo: %+v", i)
	}
	if i.Cwd != dir || i.Host == "" {
		t.Fatalf("cwd/host missing: %+v", i)
	}
}

func TestDetectRealGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("remote", "add", "origin", "git@github.com:acme/widget.git")
	if err := writeFile(filepath.Join(dir, "a.txt"), "hello"); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "first")

	i := Detect(dir)
	if i.Repo != "acme/widget" || i.Branch != "main" || len(i.Commit) < 6 {
		t.Fatalf("clean repo provenance: %+v", i)
	}
	if i.Dirty {
		t.Fatalf("clean tree reported dirty: %+v", i)
	}
	if err := writeFile(filepath.Join(dir, "a.txt"), "changed"); err != nil {
		t.Fatal(err)
	}
	if i := Detect(dir); !i.Dirty {
		t.Fatalf("uncommitted change not detected: %+v", i)
	}
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

func TestDeriveName(t *testing.T) {
	// Not a git tree: the folder name is the whole identity, no trailing dash.
	d := t.TempDir()
	scratch := filepath.Join(d, "scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DeriveName(scratch); got != "scratch" {
		t.Fatalf("non-git name = %q, want scratch", got)
	}

	// A git tree names the branch too, so two branches are two peers.
	repo := filepath.Join(d, "api")
	mustGit(t, "", "init", "-q", "-b", "main", repo)
	if err := writeFile(filepath.Join(repo, "f.txt"), "x"); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", ".")
	mustGit(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init")
	if got := DeriveName(repo); got != "api-main" {
		t.Fatalf("git name = %q, want api-main", got)
	}

	// A slashed branch is sanitized, not truncated, and stays distinct.
	mustGit(t, repo, "checkout", "-q", "-b", "feat/tokens")
	if got := DeriveName(repo); got != "api-feat-tokens" {
		t.Fatalf("branch name = %q, want api-feat-tokens", got)
	}

	// Detached HEAD has no branch; the commit identifies the tree instead.
	sha := git(repo, "rev-parse", "--short", "HEAD")
	mustGit(t, repo, "checkout", "-q", "--detach", "HEAD")
	if got := DeriveName(repo); got != "api-"+sha {
		t.Fatalf("detached name = %q, want api-%s", got, sha)
	}
}

func TestSanitizeName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"api-feature/x", "api-feature-x"},
		{"api-", "api"},
		{"a//b", "a-b"},
		{"ok.name_1", "ok.name_1"},
	} {
		if got := sanitizeName(c.in); got != c.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

