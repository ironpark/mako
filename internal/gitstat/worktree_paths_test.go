package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A config's paths may not name a .git in any case or spelling a
// case-insensitive file system or Windows reads as one.
func TestWorktreeConfigPathsRefuseDotGit(t *testing.T) {
	for _, p := range []string{".git", ".GIT/hooks", "sub/.Git/config", ".git./hooks", ".git /x", "a/../.git"} {
		if _, err := cleanWorktreeRelPath(p); err == nil {
			t.Errorf("%q taken", p)
		}
	}
	for _, p := range []string{".github", "x.git", ".gitignore", "a/.gitkeep"} {
		if _, err := cleanWorktreeRelPath(p); err != nil {
			t.Errorf("%q refused: %v", p, err)
		}
	}
}

// bootstrapRepo is a repository whose committed .mako/worktree.toml is
// config, with .gitignore ignoring ignored.
func bootstrapRepo(t *testing.T, config, ignored string) (string, func(args ...string) string) {
	t.Helper()
	root, run := cliRepo(t)
	write(t, root, "a.txt", "one\n")
	write(t, root, ".gitignore", ignored)
	write(t, root, ".mako/worktree.toml", config)
	run("add", "-A")
	run("commit", "-q", "-m", "initial")
	return root, run
}

// addRefused checks that AddWorktree refuses the worktree saying want,
// and leaves no worktree or branch behind.
func addRefused(t *testing.T, root string, run func(args ...string) string, start, want string) {
	t.Helper()
	wt, err := AddWorktree(context.Background(), root, NewWorktree{Branch: "feat/refused", Start: start})
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("AddWorktree = %+v, %v; want an error saying %q", wt, err, want)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), filepath.Base(root)+".worktrees", "feat-refused")); !os.IsNotExist(err) {
		t.Fatal("refused worktree left behind")
	}
	if strings.Contains(run("branch"), "feat/refused") {
		t.Fatal("refused worktree's branch left behind")
	}
}

// A symlink that leads nowhere is refused rather than taken for a path in
// the repository, as is one that leads out of it.
func TestWorktreeBootstrapRefusesDanglingAndEscapingSymlinks(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "secret", "s\n")
	for name, tc := range map[string]struct{ config, link, target, want string }{
		"dangling shared": {`shared_paths = ["out"]`, "out", filepath.Join(outside, "missing"), "does not resolve"},
		"dangling copy":   {`copy_paths = ["out"]`, "out", filepath.Join(outside, "missing"), "does not resolve"},
		"escaping shared": {`shared_paths = ["out"]`, "out", outside, "outside the repository"},
		"escaping copy":   {`copy_paths = ["out"]`, "out", filepath.Join(outside, "secret"), "outside the repository"},
		"nested absolute": {`copy_paths = ["conf"]`, "conf/link", filepath.Join(outside, "missing"), "leads outside"},
		"nested relative": {`copy_paths = ["conf"]`, "conf/link", "../../outside", "leads outside"},
	} {
		t.Run(name, func(t *testing.T) {
			root, run := bootstrapRepo(t, tc.config+"\n", "out\nconf/\n")
			write(t, root, "conf/ok", "x\n")
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, tc.link)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.target, filepath.Join(root, tc.link)); err != nil {
				t.Fatal(err)
			}
			addRefused(t, root, run, "main", tc.want)
		})
	}
}

// A path that resolves to the repository's .git or its root is refused:
// linking or copying it would hand the worktree the main repository's
// hooks and config.
func TestWorktreeBootstrapRefusesSymlinksToDotGitOrTheRoot(t *testing.T) {
	for name, tc := range map[string]struct{ config, link, target, want string }{
		"shared to .git":   {`shared_paths = ["gl"]`, "gl", ".git", "outside the repository"},
		"shared to root":   {`shared_paths = ["self"]`, "self", ".", "outside the repository"},
		"shared into .git": {`shared_paths = ["hooks"]`, "hooks", ".git/hooks", "outside the repository"},
		"copy to .git":     {`copy_paths = ["gl"]`, "gl", ".git", "outside the repository"},
		"nested to root":   {`copy_paths = ["conf"]`, "conf/up", "..", "leads outside"},
		"nested to .git":   {`copy_paths = ["conf"]`, "conf/g", "../.GIT", "leads outside"},
	} {
		t.Run(name, func(t *testing.T) {
			root, run := bootstrapRepo(t, tc.config+"\n", "gl\nself\nhooks\nconf/\n")
			write(t, root, "conf/ok", "x\n")
			if err := os.Symlink(tc.target, filepath.Join(root, tc.link)); err != nil {
				t.Fatal(err)
			}
			addRefused(t, root, run, "main", tc.want)
		})
	}
}

// A symlink the checked-out branch has on the way to a path is refused, so
// nothing is linked or copied through it to outside the worktree.
func TestWorktreeBootstrapRefusesSymlinksInTheWorktree(t *testing.T) {
	outside := t.TempDir()
	for name, config := range map[string]string{
		"copy":   `copy_paths = ["conf/secret.env"]`,
		"shared": `shared_paths = ["conf/cache"]`,
	} {
		t.Run(name, func(t *testing.T) {
			root, run := bootstrapRepo(t, config+"\n", "conf/\n")
			// A branch whose conf is a symlink out of the worktree.
			run("checkout", "-q", "-b", "evil")
			if err := os.Symlink(outside, filepath.Join(root, "conf")); err != nil {
				t.Fatal(err)
			}
			run("add", "-f", "conf")
			run("commit", "-q", "-m", "conf leads out")
			run("checkout", "-q", "main")
			write(t, root, "conf/secret.env", "SECRET=1\n")
			write(t, root, "conf/cache/x", "x\n")

			addRefused(t, root, run, "evil", "traverses a symbolic link")
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Fatalf("written outside the worktree: %v", entries)
			}
		})
	}
}

// What copy_paths copies keeps its relative symlinks that stay in the
// repository's files.
func TestWorktreeBootstrapCopiesInnerSymlinks(t *testing.T) {
	root, _ := bootstrapRepo(t, "copy_paths = [\"conf\"]\n", "conf/\n")
	write(t, root, "conf/dev.json", "{}\n")
	if err := os.Symlink("dev.json", filepath.Join(root, "conf", "current.json")); err != nil {
		t.Fatal(err)
	}
	wt, err := AddWorktree(context.Background(), root, NewWorktree{Branch: "feat/links", Start: "main"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(wt.Path)) })
	if target, err := os.Readlink(filepath.Join(wt.Path, "conf", "current.json")); err != nil || target != "dev.json" {
		t.Fatalf("link = %q, %v", target, err)
	}
	if b, err := os.ReadFile(filepath.Join(wt.Path, "conf", "current.json")); err != nil || string(b) != "{}\n" {
		t.Fatalf("through the link: %q, %v", b, err)
	}
}
