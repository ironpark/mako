package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/repotrust"
)

// trustAll trusts every repository's commands, as a user who trusted
// them all would.
func trustAll(string, string, []byte) bool { return true }

func TestWorktreeBootstrapTOML(t *testing.T) {
	root, run := cliRepo(t)
	ctx := context.Background()

	write(t, root, "a.txt", "one\n")
	write(t, root, ".gitignore", ".env\nnode_modules/\nsetup.marker\n")
	write(t, root, ".mako/worktree.toml", `
shared_paths = ["node_modules"]
copy_paths = [".env", "config/local"]
setup_cmd = "printf '%s|%s' \"$MAKO_WORKTREE_BRANCH\" \"$MAKO_WORKTREE_BASE\" > setup.marker"
teardown_cmd = "printf 'torn-down' > \"$MAKO_WORKTREE_REPO/teardown.marker\""
`)
	run("add", "-A")
	run("commit", "-q", "-m", "initial")

	// Untracked/gitignored assets in the main repo that worktrees should inherit.
	write(t, root, "node_modules/pkg/index.js", "export const ok = true;\n")
	write(t, root, ".env", "SECRET=42\n")
	write(t, root, "config/local/dev.json", `{"port":3000}`)

	wt, err := AddWorktree(ctx, root, NewWorktree{Branch: "feat/boot", Start: "main", Trusted: trustAll})
	if err != nil {
		t.Fatal(err)
	}
	repo, path, made := wt.Repo, wt.Path, wt.Branch
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(path)) })
	if wt.SkippedHooks != "" {
		t.Fatalf("trusted setup_cmd reported skipped: %q", wt.SkippedHooks)
	}

	// shared_paths must be symlinked to main repo.
	nm := filepath.Join(path, "node_modules")
	fi, err := os.Lstat(nm)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("node_modules should be symlink, err=%v mode=%v", err, fi)
	}
	if data, err := os.ReadFile(filepath.Join(nm, "pkg", "index.js")); err != nil || !strings.Contains(string(data), "ok = true") {
		t.Fatalf("symlinked node_modules content: %q %v", data, err)
	}

	// copy_paths must be copied independently (not symlinked).
	envPath := filepath.Join(path, ".env")
	if fi, err := os.Lstat(envPath); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf(".env should be a regular file copy, err=%v mode=%v", err, fi)
	}
	if data, err := os.ReadFile(envPath); err != nil || string(data) != "SECRET=42\n" {
		t.Fatalf(".env content = %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(path, "config/local/dev.json")); err != nil || string(data) != `{"port":3000}` {
		t.Fatalf("copied dir file = %q, %v", data, err)
	}

	// setup_cmd must have executed with env vars.
	if marker, err := os.ReadFile(filepath.Join(path, "setup.marker")); err != nil || string(marker) != made+"|main" {
		t.Fatalf("setup.marker = %q, err=%v", marker, err)
	}

	// The copy of config/local, not ignored, is taken out with the rest.
	w := &protocol.Worktree{Repo: repo, Path: path, Branch: made, Base: "main"}
	if _, err := RemoveWorktree(ctx, w, RemoveWorktreeOptions{Trusted: trustAll}); err != nil {
		t.Fatalf("RemoveWorktree failed: %v", err)
	}
	if marker, err := os.ReadFile(filepath.Join(root, "teardown.marker")); err != nil || string(marker) != "torn-down" {
		t.Fatalf("teardown.marker = %q, err=%v", marker, err)
	}
}

func TestWorktreeBootstrapOrcaFallback(t *testing.T) {
	root, run := cliRepo(t)
	ctx := context.Background()
	write(t, root, "a.txt", "one\n")
	write(t, root, ".gitignore", ".cache/\n")
	write(t, root, "orca.yaml", "worktree:\n  sharedDirectories:\n    - .cache\n")
	run("add", "-A")
	run("commit", "-q", "-m", "initial")

	write(t, root, ".cache/data.bin", "cached")
	wt, err := AddWorktree(ctx, root, NewWorktree{Branch: "feat/orca", Start: "main", Trusted: trustAll})
	if err != nil {
		t.Fatal(err)
	}
	path := wt.Path
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(path)) })

	fi, err := os.Lstat(filepath.Join(path, ".cache"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".cache should be symlinked from orca.yaml fallback, err=%v", err)
	}
}

func TestWorktreeBootstrapFailureRollsBack(t *testing.T) {
	root, run := cliRepo(t)
	ctx := context.Background()
	write(t, root, "a.txt", "one\n")
	write(t, root, ".mako/worktree.toml", "setup_cmd = \"exit 1\"\n")
	run("add", "-A")
	run("commit", "-q", "-m", "initial")

	wt, err := AddWorktree(ctx, root, NewWorktree{Branch: "feat/fail-setup", Start: "main", Trusted: trustAll})
	if err == nil || !strings.Contains(err.Error(), "setup_cmd failed") {
		t.Fatalf("expected setup_cmd failure, got path=%q err=%v", wt.Path, err)
	}
	expectedPath := filepath.Join(filepath.Dir(root), filepath.Base(root)+".worktrees", "feat-fail-setup")
	if _, statErr := os.Stat(expectedPath); !os.IsNotExist(statErr) {
		t.Fatalf("failed worktree should have been rolled back: %s", expectedPath)
	}
	if strings.Contains(run("branch"), "feat/fail-setup") {
		t.Fatalf("failed worktree branch should have been deleted")
	}
}

func TestWorktreeConfigRejectsPathTraversal(t *testing.T) {
	root, run := cliRepo(t)
	write(t, root, "a.txt", "one\n")
	write(t, root, ".mako/worktree.toml", "shared_paths = [\"../secret\"]\n")
	run("add", "-A")
	run("commit", "-q", "-m", "initial")

	if _, err := LoadWorktreeConfig(root); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

// A repository's setup_cmd and teardown_cmd run only while the user
// trusts its .mako/worktree.toml as it reads: untrusted, or changed since
// it was trusted, the worktree is made and removed without them, and says
// so with what to run to trust it.
func TestWorktreeHooksRunOnlyWhileTrusted(t *testing.T) {
	root, run := cliRepo(t)
	ctx := context.Background()
	write(t, root, ".gitignore", ".env\nsetup.marker\n")
	config := "copy_paths = [\".env\"]\nsetup_cmd = \"touch setup.marker\"\nteardown_cmd = \"touch \\\"$MAKO_WORKTREE_REPO/teardown.marker\\\"\"\n"
	write(t, root, ".mako/worktree.toml", config)
	run("add", "-A")
	run("commit", "-q", "-m", "initial")
	write(t, root, ".env", "SECRET=1\n")
	store := repotrust.Open(t.TempDir())

	check := func(branch string, wantRun, wantSkipped bool) MadeWorktree {
		t.Helper()
		wt, err := AddWorktree(ctx, root, NewWorktree{Branch: branch, Start: "main", Trusted: store.Trusted})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(filepath.Dir(wt.Path)) })
		if _, err := os.Stat(filepath.Join(wt.Path, ".env")); err != nil {
			t.Fatalf("copy_paths not copied without trust: %v", err)
		}
		_, err = os.Stat(filepath.Join(wt.Path, "setup.marker"))
		if ran := err == nil; ran != wantRun {
			t.Fatalf("%s: setup_cmd ran = %v, want %v", branch, ran, wantRun)
		}
		if skipped := wt.SkippedHooks != ""; skipped != wantSkipped {
			t.Fatalf("%s: skipped = %q", branch, wt.SkippedHooks)
		}
		if wantSkipped && wt.SkippedHooks != filepath.Join(wt.Repo, ".mako", "worktree.toml") {
			t.Fatalf("skipped = %q, not the repository's file", wt.SkippedHooks)
		}
		return wt
	}

	untrusted := check("feat/untrusted", false, true)
	w := &protocol.Worktree{Repo: untrusted.Repo, Path: untrusted.Path, Branch: untrusted.Branch, Base: "main"}
	out, err := RemoveWorktree(ctx, w, RemoveWorktreeOptions{Trusted: store.Trusted})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "teardown_cmd") || !strings.Contains(out, "mako repo-trust allow "+filepath.Join(untrusted.Repo, ".mako", "worktree.toml")) {
		t.Fatalf("removal does not say teardown_cmd was skipped: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "teardown.marker")); err == nil {
		t.Fatal("untrusted teardown_cmd ran")
	}

	// Trusted as it reads, both run.
	if _, err := store.Trust(root, WorktreeConfigFile, repotrust.Hash([]byte(config))); err != nil {
		t.Fatal(err)
	}
	trusted := check("feat/trusted", true, false)
	w = &protocol.Worktree{Repo: trusted.Repo, Path: trusted.Path, Branch: trusted.Branch, Base: "main"}
	if out, err := RemoveWorktree(ctx, w, RemoveWorktreeOptions{Trusted: store.Trusted}); err != nil || strings.Contains(out, "teardown_cmd") {
		t.Fatalf("trusted removal = %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(root, "teardown.marker")); err != nil {
		t.Fatal("trusted teardown_cmd did not run")
	}

	// Changed since it was trusted, it is not trusted.
	write(t, root, ".mako/worktree.toml", config+"# more\n")
	check("feat/changed", false, true)

	// The kill switch runs none, trusted or not, and says nothing of it.
	write(t, root, ".mako/worktree.toml", config)
	t.Setenv("MAKO_NO_WORKTREE_HOOKS", "1")
	check("feat/off", false, false)
}

// A worktree whose setup_cmd ran out the request's time is still rolled
// back: undoing it does not run on the context that ended.
func TestWorktreeBootstrapRollsBackAfterTheContextEnds(t *testing.T) {
	root, run := cliRepo(t)
	write(t, root, "a.txt", "one\n")
	write(t, root, ".mako/worktree.toml", "setup_cmd = \"sleep 30\"\n")
	run("add", "-A")
	run("commit", "-q", "-m", "initial")

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := AddWorktree(ctx, root, NewWorktree{Branch: "feat/slow", Start: "main", Trusted: trustAll}); err == nil {
		t.Fatal("a setup_cmd past the deadline succeeded")
	}
	if took := time.Since(started); took > 20*time.Second {
		t.Fatalf("took %v after the deadline", took)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), filepath.Base(root)+".worktrees", "feat-slow")); !os.IsNotExist(err) {
		t.Fatal("worktree left behind")
	}
	if strings.Contains(run("branch"), "feat/slow") {
		t.Fatal("branch left behind")
	}
}

// A folder whose .git points into a trusted repository that does not list
// it as a worktree is not of that repository: no worktree of it is made
// from there, so its trusted setup_cmd does not run, and the repository's
// config is not read for the folder. From a worktree the repository lists,
// it runs.
func TestSpoofedFolderRunsNoTrustedSetup(t *testing.T) {
	root, _ := bootstrapRepo(t, "setup_cmd = \"touch \\\"$MAKO_WORKTREE_REPO/setup-$MAKO_WORKTREE_BRANCH\\\"\"\n", "setup-*\n")
	ctx := context.Background()
	real, err := AddWorktree(ctx, root, NewWorktree{Branch: "real", Start: "main", Trusted: trustAll})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(real.Path)) })
	gitDir := strings.TrimSpace(string(must(gitOut(ctx, real.Path, nil, "rev-parse", "--path-format=absolute", "--git-dir"))))

	spoof := t.TempDir()
	write(t, spoof, ".git", "gitdir: "+gitDir+"\n")
	if _, err := AddWorktree(ctx, spoof, NewWorktree{Branch: "spoofed", Trusted: trustAll}); err == nil || !strings.Contains(err.Error(), "no worktree of it is made") {
		t.Fatalf("a worktree made from a spoofed folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "setup-spoofed")); err == nil {
		t.Fatal("the trusted setup_cmd ran for a spoofed folder")
	}
	if _, err := gitOut(ctx, root, nil, "show-ref", "--verify", "--quiet", "refs/heads/spoofed"); err == nil {
		t.Fatal("a branch was made for a spoofed folder")
	}
	if cfg, err := LoadWorktreeConfig(spoof); err != nil || cfg.SetupCmd != "" {
		t.Fatalf("the repository's config read for a spoofed folder: %+v, %v", cfg, err)
	}

	from, err := AddWorktree(ctx, real.Path, NewWorktree{Branch: "listed", Trusted: trustAll})
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(from.Repo, root) {
		t.Fatalf("repo %s, want %s", from.Repo, root)
	}
	if _, err := os.Stat(filepath.Join(root, "setup-listed")); err != nil {
		t.Fatal("the trusted setup_cmd did not run for a worktree the repository lists")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
