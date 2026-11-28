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

// A folder is a linked work tree of a repository only when the repository
// lists it as one, both ways. A .git file or a commondir file that points
// into a repository's git directory, as anyone can write one, leaves the
// folder its own, so nothing checked by the repository's root, such as its
// trusted commands, is taken for it.
func TestRepoTakesOnlyAWorktreeItsRepositoryLists(t *testing.T) {
	root, run := cliRepo(t)
	write(t, root, "a.txt", "a\n")
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	want, _ := filepath.EvalSymlinks(root)
	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "-b", "other", linked)
	// git 2.48 and later can record a worktree by relative paths.
	relative := filepath.Join(t.TempDir(), "relative")
	run("-c", "worktree.useRelativePaths=true", "worktree", "add", "-q", "-b", "rel", relative)
	for _, dir := range []string{linked, relative} {
		if got := Repo(dir); got != want {
			t.Errorf("Repo(%s) = %q, want %q", dir, got, want)
		}
		if top, repo := WorkTree(filepath.Join(dir, ".")); top != dir || repo != want {
			t.Errorf("WorkTree(%s) = %q, %q", dir, top, repo)
		}
	}
	gitDir := filepath.Join(root, ".git", "worktrees", "linked")

	// A .git file naming the linked work tree's git directory.
	spoof := t.TempDir()
	write(t, spoof, ".git", "gitdir: "+gitDir+"\n")
	// A git directory of its own whose commondir is the repository's.
	fake := t.TempDir()
	write(t, fake, ".git", "gitdir: "+filepath.Join(fake, "g")+"\n")
	write(t, fake, "g/HEAD", "ref: refs/heads/main\n")
	write(t, fake, "g/commondir", filepath.Join(root, ".git")+"\n")
	write(t, fake, "g/gitdir", filepath.Join(fake, ".git")+"\n")
	// A .git folder whose commondir is the repository's.
	folder := t.TempDir()
	write(t, folder, ".git/HEAD", "ref: refs/heads/main\n")
	write(t, folder, ".git/commondir", filepath.Join(root, ".git")+"\n")
	dirs := []string{spoof, fake, folder}
	// A link to the linked work tree's own .git file.
	link := t.TempDir()
	if err := os.Symlink(filepath.Join(linked, ".git"), filepath.Join(link, ".git")); err == nil {
		dirs = append(dirs, link)
	}
	for _, dir := range dirs {
		own, _ := filepath.EvalSymlinks(dir)
		if got := Repo(dir); got != own {
			t.Errorf("Repo(%s) = %q, want the folder itself", dir, got)
		}
		if _, repo, _, _ := Head(dir); repo != own {
			t.Errorf("Head(%s) names %q, want the folder itself", dir, repo)
		}
	}
}

// A repository is named the same however its remote reaches it.
func TestTheRemoteNamesTheRepository(t *testing.T) {
	for url, want := range map[string]string{
		"git@github.com:ironpark/ultrasync.git":            "github.com/ironpark/ultrasync",
		"https://github.com/IronPark/UltraSync":            "github.com/ironpark/ultrasync",
		"ssh://git@Codeberg.org:2222/forgejo/forgejo.git":  "codeberg.org/forgejo/forgejo",
		"https://user:token@git.example.com:3000/team/app": "git.example.com/team/app",
		"https://gitlab.com/group/sub/project.git":         "gitlab.com/group/sub/project",
		"/srv/git/team/app.git":                            "",
		"file:///srv/git/team/app.git":                     "",
		"":                                                 "",
	} {
		if got := urlRemote(url); got != want {
			t.Errorf("urlRemote(%q) = %q, want %q", url, got, want)
		}
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
		if got := urlOwner(originURL(dir)); got != want {
			t.Errorf("owner of %q = %q, want %q", config, got, want)
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
		if _, _, owner, remote := Head(dir); owner != "github.com/ironpark" || remote != "github.com/ironpark/mako" {
			t.Errorf("Head(%s): owner %q, remote %q", dir, owner, remote)
		}
	}
}
