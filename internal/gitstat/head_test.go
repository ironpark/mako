package gitstat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBranchReadsHEAD(t *testing.T) {
	root, run := cliRepo(t)
	if b := Branch(root); b != "main" {
		t.Fatalf("unborn branch %q", b)
	}
	write(t, root, "a.txt", "a\n")
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	run("switch", "-q", "-c", "feature/x")
	if b := Branch(sub); b != "feature/x" {
		t.Fatalf("from a subfolder %q", b)
	}
	// A linked work tree's .git is a file pointing at its git directory.
	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "-b", "other", linked)
	if b := Branch(linked); b != "other" {
		t.Fatalf("linked work tree %q", b)
	}
	run("switch", "-q", "--detach")
	if b := Branch(root); len(b) != 7 {
		t.Fatalf("detached %q", b)
	}
	if b := Branch(t.TempDir()); b != "" {
		t.Fatalf("outside a repository %q", b)
	}
}

// Repo names one folder for a repository's subfolders and its linked work
// trees: the main work tree.
func TestRepoIsTheMainWorkTree(t *testing.T) {
	root, run := cliRepo(t)
	write(t, root, "a.txt", "a\n")
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	sub := filepath.Join(root, "sub", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "-b", "other", linked)
	inside := filepath.Join(root, ".claude", "worktrees", "x")
	run("worktree", "add", "-q", "-b", "inside", inside)
	want, _ := filepath.EvalSymlinks(root) // as git records it for the linked ones
	for _, dir := range []string{root, sub, linked, filepath.Join(linked, "."), inside} {
		if got := Repo(dir); got != want {
			t.Errorf("Repo(%s) = %q, want %q", dir, got, want)
		}
	}
	if got := Repo(t.TempDir()); got != "" {
		t.Errorf("outside a repository %q", got)
	}
}

func TestOwnerOfTheOriginRemote(t *testing.T) {
	for url, want := range map[string]string{
		"git@github.com:ironpark/mako.git":                 "github.com/ironpark",
		"ssh://git@git.ironpark.dev/ironpark/mako.git":     "git.ironpark.dev/ironpark",
		"ssh://git@Codeberg.org:2222/forgejo/forgejo.git":  "codeberg.org/forgejo",
		"https://user:token@git.example.com:3000/team/app": "git.example.com/team",
		"https://github.com/golang/go":                     "github.com/golang",
		"https://gitlab.com/group/sub/project.git":         "gitlab.com/sub",
		"file:///srv/git/team/app.git":                     "team",
		"/srv/git/team/app.git":                            "team",
		`C:\repos\team\app`:                                "team",
		"https://example.com/lonely.git":                   "",
		"":                                                 "",
	} {
		if got := urlOwner(url); got != want {
			t.Errorf("urlOwner(%q) = %q, want %q", url, got, want)
		}
	}
	// The origin, or without one the first remote by name.
	for config, want := range map[string]string{
		"[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = git@github.com:golang/go.git\n[remote \"origin\"]\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n\turl = git@github.com:ironpark/go.git\n": "github.com/ironpark",
		"[remote \"upstream\"]\n\turl = https://h/b/c\n[remote \"fork\"]\n\turl = https://h/a/c\n":                                                                                                          "h/a",
		"[core]\n\tbare = false\n": "",
	} {
		dir := t.TempDir()
		write(t, dir, "config", config)
		if got := ownerOf(dir); got != want {
			t.Errorf("ownerOf(%q) = %q, want %q", config, got, want)
		}
	}

	root, run := cliRepo(t)
	run("remote", "add", "origin", "git@github.com:ironpark/mako.git")
	write(t, root, "a.txt", "a\n")
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "-b", "other", linked)
	for _, dir := range []string{root, linked} {
		if _, _, got := Head(dir); got != "github.com/ironpark" {
			t.Errorf("Owner(%s) = %q", dir, got)
		}
	}
}
