package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// removeRepo is a repository whose worktrees link deps/cache, a nested
// shared path, and copy conf, neither ignored, and run teardown as given;
// and a worktree of it, bootstrapped.
func removeRepo(t *testing.T, teardown string) (root string, w *protocol.Worktree) {
	t.Helper()
	root, _ = bootstrapRepo(t, "shared_paths = [\"deps/cache\"]\ncopy_paths = [\"conf\"]\nteardown_cmd = "+teardown+"\n", "")
	write(t, root, "deps/cache/x", "x\n")
	write(t, root, "conf/dev.json", "{}\n")
	made, err := AddWorktree(context.Background(), root, NewWorktree{Branch: "feat/rm", Start: "main"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(made.Path)) })
	return root, &protocol.Worktree{Repo: made.Repo, Path: made.Path, Branch: made.Branch, Base: "main"}
}

// bootstrapped checks that w still has what its bootstrap made.
func bootstrapped(t *testing.T, root string, w *protocol.Worktree) {
	t.Helper()
	if target, err := os.Readlink(filepath.Join(w.GetPath(), "deps", "cache")); err != nil || target != filepath.Join(w.GetRepo(), "deps", "cache") {
		t.Fatalf("shared link = %q, %v", target, err)
	}
	if b, err := os.ReadFile(filepath.Join(w.GetPath(), "conf", "dev.json")); err != nil || string(b) != "{}\n" {
		t.Fatalf("copy = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, "deps", "cache", "x")); err != nil {
		t.Fatal("the shared folder lost its files")
	}
}

// What the bootstrap made, as it made it, is taken out with the worktree,
// nested folders and copies not ignored too, and the shared folder keeps
// its files.
func TestRemoveWorktreeTakesOutWhatTheBootstrapMade(t *testing.T) {
	root, w := removeRepo(t, `""`)
	bootstrapped(t, root, w)
	if _, err := RemoveWorktree(context.Background(), w, RemoveWorktreeOptions{Trusted: trustAll}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.GetPath()); !os.IsNotExist(err) {
		t.Fatal("worktree left")
	}
	if b, err := os.ReadFile(filepath.Join(root, "deps", "cache", "x")); err != nil || string(b) != "x\n" {
		t.Fatalf("the shared folder's file = %q, %v", b, err)
	}
}

// A worktree with work in it is refused before its teardown_cmd runs or
// anything of it is taken out: a tracked change, a new file, or a copy
// changed since it was made.
func TestRemoveWorktreeRefusesChangesFirst(t *testing.T) {
	for name, change := range map[string]func(path string){
		"tracked":     func(path string) { write(t, path, "a.txt", "changed\n") },
		"new file":    func(path string) { write(t, path, "notes.txt", "mine\n") },
		"copy edited": func(path string) { write(t, path, "conf/dev.json", "{\"port\":1}\n") },
		"copy added":  func(path string) { write(t, path, "conf/more.json", "{}\n") },
	} {
		t.Run(name, func(t *testing.T) {
			root, w := removeRepo(t, `"touch \"$MAKO_WORKTREE_REPO/teardown.marker\""`)
			change(w.GetPath())
			if _, err := RemoveWorktree(context.Background(), w, RemoveWorktreeOptions{Trusted: trustAll}); err == nil || !strings.Contains(err.Error(), "commit or remove them first") {
				t.Fatalf("removed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "teardown.marker")); err == nil {
				t.Fatal("teardown_cmd ran for a worktree that stays")
			}
			if _, err := os.Readlink(filepath.Join(w.GetPath(), "deps", "cache")); err != nil {
				t.Fatalf("shared link taken out of a worktree that stays: %v", err)
			}
		})
	}
}

// A failing teardown_cmd does not keep the worktree: it is removed, and
// the output says the teardown failed and what it printed.
func TestRemoveWorktreeAfterAFailingTeardown(t *testing.T) {
	_, w := removeRepo(t, `"echo cleaning up; exit 3"`)
	out, err := RemoveWorktree(context.Background(), w, RemoveWorktreeOptions{Trusted: trustAll})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cleaning up") || !strings.HasSuffix(out, "warning: teardown_cmd failed (exit status 3); the worktree was removed anyway") {
		t.Fatalf("output = %q", out)
	}
	if _, err := os.Stat(w.GetPath()); !os.IsNotExist(err) {
		t.Fatal("worktree left")
	}
}

// When git still refuses the worktree, after its teardown_cmd left a file
// in it, what the bootstrap made is put back.
func TestRemoveWorktreeRefusedPutsTheBootstrapBack(t *testing.T) {
	root, w := removeRepo(t, `"touch stray"`)
	if _, err := RemoveWorktree(context.Background(), w, RemoveWorktreeOptions{Trusted: trustAll}); err == nil {
		t.Fatal("removed a worktree with a stray file")
	}
	bootstrapped(t, root, w)
}

// A removal names its worktree, which must be one git has of the
// repository it names, beside its main work tree; and its branch a
// branch's name. Anything else is refused before its teardown_cmd runs or
// anything is taken out.
func TestRemoveWorktreeRefusesWhatGitDoesNotHave(t *testing.T) {
	root, w := removeRepo(t, `"touch \"$MAKO_WORKTREE_REPO/teardown.marker\""`)
	other, _ := bootstrapRepo(t, "", "")
	// So that @{-1} reads as a branch: the one checked out before main.
	for _, args := range [][]string{{"checkout", "-q", "-b", "previous"}, {"checkout", "-q", "main"}} {
		if _, err := gitOut(context.Background(), root, nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		w    *protocol.Worktree
		want string
	}{
		"another repository's folder": {&protocol.Worktree{Repo: root, Path: other, Branch: "feat/rm"}, "is not a worktree of"},
		"the main work tree":          {&protocol.Worktree{Repo: root, Path: root, Branch: "feat/rm"}, "is not a worktree of"},
		"a folder of the worktree":    {&protocol.Worktree{Repo: root, Path: filepath.Join(w.GetPath(), "conf"), Branch: "feat/rm"}, "is not a worktree of"},
		"a worktree for repository":   {&protocol.Worktree{Repo: w.GetPath(), Path: w.GetPath(), Branch: "feat/rm"}, "is not the main work tree"},
		"another repository":          {&protocol.Worktree{Repo: other, Path: w.GetPath(), Branch: "feat/rm"}, "is not a worktree of"},
		"an option for a branch":      {&protocol.Worktree{Repo: root, Path: w.GetPath(), Branch: "-D"}, "is not a branch name"},
		"a branch git reads as other": {&protocol.Worktree{Repo: root, Path: w.GetPath(), Branch: "@{-1}"}, "is not a branch name"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RemoveWorktree(context.Background(), tc.w, RemoveWorktreeOptions{Trusted: trustAll}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one saying %q", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(root, "teardown.marker")); err == nil {
				t.Fatal("teardown_cmd ran")
			}
			bootstrapped(t, root, w)
		})
	}
	if _, err := RemoveWorktree(context.Background(), w, RemoveWorktreeOptions{Trusted: trustAll}); err != nil {
		t.Fatal(err)
	}
}

// Forced, a removal takes the worktree with its changes, and its branch
// though not merged.
func TestRemoveWorktreeForced(t *testing.T) {
	root, w := removeRepo(t, `""`)
	write(t, w.GetPath(), "a.txt", "changed\n")
	write(t, w.GetPath(), "notes.txt", "mine\n")
	run := func(args ...string) {
		t.Helper()
		if _, err := gitOut(context.Background(), w.GetPath(), nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	run("-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "not merged")
	if _, err := RemoveWorktree(context.Background(), w, RemoveWorktreeOptions{Trusted: trustAll}); err == nil {
		t.Fatal("removed a worktree with changes unforced")
	}
	if _, err := RemoveWorktree(context.Background(), w, RemoveWorktreeOptions{Force: true, Trusted: trustAll}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.GetPath()); !os.IsNotExist(err) {
		t.Fatal("worktree left")
	}
	if _, err := gitOut(context.Background(), root, nil, "show-ref", "--verify", "--quiet", "refs/heads/feat/rm"); err == nil {
		t.Fatal("branch left")
	}
	if b, err := os.ReadFile(filepath.Join(root, "deps", "cache", "x")); err != nil || string(b) != "x\n" {
		t.Fatalf("the shared folder's file = %q, %v", b, err)
	}
}
