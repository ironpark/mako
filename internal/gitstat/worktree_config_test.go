package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

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

	repo, path, made, err := AddWorktree(ctx, root, "feat/boot", "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(path)) })

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

	// Clean up untracked copy_paths dir that wasn't gitignored before removal.
	_ = os.RemoveAll(filepath.Join(path, "config"))
	w := &protocol.Worktree{Repo: repo, Path: path, Branch: made, Base: "main"}
	if _, err := RemoveWorktree(ctx, w); err != nil {
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
	_, path, _, err := AddWorktree(ctx, root, "feat/orca", "main")
	if err != nil {
		t.Fatal(err)
	}
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

	_, path, _, err := AddWorktree(ctx, root, "feat/fail-setup", "main")
	if err == nil || !strings.Contains(err.Error(), "setup_cmd failed") {
		t.Fatalf("expected setup_cmd failure, got path=%q err=%v", path, err)
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
