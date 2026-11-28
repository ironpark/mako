package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baseRepo is a repository with commits on main and dev, dev checked out,
// and an origin whose main is a commit of its own; and the commit each of
// them is at.
func baseRepo(t *testing.T, config string) (root string, run func(args ...string) string, at map[string]string) {
	t.Helper()
	root, run = cliRepo(t)
	write(t, root, "a.txt", "main\n")
	if config != "" {
		write(t, root, ".mako/worktree.toml", config)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "main")
	run("commit", "-q", "--allow-empty", "-m", "origin main")
	run("remote", "add", "origin", t.TempDir())
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("reset", "-q", "--hard", "HEAD~")
	run("checkout", "-q", "-b", "dev")
	run("commit", "-q", "--allow-empty", "-m", "dev")
	at = map[string]string{}
	for _, ref := range []string{"main", "dev", "origin/main"} {
		at[ref] = strings.TrimSpace(run("rev-parse", ref))
	}
	return root, run, at
}

// addAt makes a worktree from dir, and checks it starts at the commit
// want and records base.
func addAt(t *testing.T, dir string, w NewWorktree, want, base string) MadeWorktree {
	t.Helper()
	made, err := AddWorktree(context.Background(), dir, w)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(made.Path)) })
	head, err := gitOut(context.Background(), made.Path, nil, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(head)); got != want {
		t.Fatalf("%+v: starts at %s, want %s", w, got, want)
	}
	if made.Base != base {
		t.Fatalf("%+v: base %q, want %q", w, made.Base, base)
	}
	return made
}

// The repository's base_ref is read from its root, from any folder of
// it, and gives a worktree's start, and its base, the local branch it
// follows, unless the caller names its own start.
func TestWorktreeBaseRef(t *testing.T) {
	root, _, at := baseRepo(t, "base_ref = \"origin/main\"\n")
	sub := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if cfg, err := LoadWorktreeConfig(sub); err != nil || cfg.BaseRef != "origin/main" {
		t.Fatalf("config from a folder of the repository: %+v, %v", cfg, err)
	}
	made := addAt(t, sub, NewWorktree{Branch: "w1"}, at["origin/main"], "main")
	if made.Start != "origin/main" {
		t.Fatalf("start %q", made.Start)
	}
	if cfg, err := LoadWorktreeConfig(made.Path); err != nil || cfg.BaseRef != "origin/main" {
		t.Fatalf("config from a worktree: %+v, %v", cfg, err)
	}
	addAt(t, root, NewWorktree{Branch: "w2", Start: "dev"}, at["dev"], "dev")
	addAt(t, root, NewWorktree{Branch: "w3", Start: at["main"]}, at["main"], "dev")
}

// Without a start or a base_ref, a worktree starts at the HEAD of the
// folder it is made from, and records that folder's branch, even when
// the folder is a worktree on another branch than the main one.
func TestWorktreeStartsAtTheFoldersHead(t *testing.T) {
	root, _, at := baseRepo(t, "")
	addAt(t, root, NewWorktree{Branch: "w1"}, at["dev"], "dev")
	other := addAt(t, root, NewWorktree{Branch: "w2", Start: "main"}, at["main"], "main")
	made := addAt(t, other.Path, NewWorktree{Branch: "w3"}, at["main"], "w2")
	if made.Start != "" || made.Repo != other.Repo {
		t.Fatalf("made %+v", made)
	}
}

func TestLocalBranch(t *testing.T) {
	root, run, at := baseRepo(t, "")
	run("tag", "v1", "main")
	run("remote", "add", "up/stream", t.TempDir())
	run("update-ref", "refs/remotes/up/stream/feat/x", "main")
	ctx := context.Background()
	for ref, want := range map[string]string{
		"main": "main", "dev": "dev", "origin/main": "main", "up/stream/feat/x": "feat/x",
		"v1": "", at["main"]: "", "": "", "origin/HEAD": "",
	} {
		if got := localBranch(ctx, root, ref); got != want {
			t.Errorf("localBranch(%q) = %q, want %q", ref, got, want)
		}
	}
}

// The setup_cmd is told of the base the worktree records, which the
// teardown_cmd is told of later: on a default launch, the branch dir has
// checked out, and the caller's base when it names one.
func TestWorktreeSetupIsToldItsBase(t *testing.T) {
	root, _, at := baseRepo(t, "setup_cmd = \"printf %s \\\"$MAKO_WORKTREE_BASE\\\" > \\\"$MAKO_WORKTREE_REPO/base-$MAKO_WORKTREE_BRANCH\\\"\"\n")
	told := func(branch string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, "base-"+branch))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	made := addAt(t, root, NewWorktree{Branch: "w1", Trusted: trustAll}, at["dev"], "dev")
	if got := told("w1"); got != made.Base {
		t.Fatalf("setup_cmd told base %q, the worktree records %q", got, made.Base)
	}
	addAt(t, root, NewWorktree{Branch: "w2", Start: "origin/main", Trusted: trustAll}, at["origin/main"], "main")
	if got := told("w2"); got != "main" {
		t.Fatalf("setup_cmd told base %q, want main", got)
	}
	addAt(t, root, NewWorktree{Branch: "w3", Start: "origin/main", Base: "dev", Trusted: trustAll}, at["origin/main"], "dev")
	if got := told("w3"); got != "dev" {
		t.Fatalf("setup_cmd told base %q, want the caller's dev", got)
	}
}
