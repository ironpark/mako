package gui

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/daemon"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/repotrust"
)

// linkedTestModel is a window linked to a daemon of its own, its
// asynchronous replies run when the test takes them from posted.
func linkedTestModel(t *testing.T) (m *model, home string, posted chan func()) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("MAKO_HOME", home)
	cfg := config.Default()
	cfg.P2P.Disabled = true
	cfg.Discovery = config.Discovery{}
	d := daemon.New(cfg, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-d.Ready():
	case err := <-done:
		t.Fatalf("daemon exited before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not become ready")
	}
	m = newModel(cfg, d.Socket())
	m.link.setEmbedded(d.Socket())
	m.link.setLinked(true)
	t.Cleanup(func() { m.link.hangUp() })
	posted = make(chan func(), 16)
	m.post = func(f func()) { posted <- f }
	return m, home, posted
}

// runPosted runs the next reply the window was posted.
func runPosted(t *testing.T, posted chan func()) {
	t.Helper()
	select {
	case f := <-posted:
		f()
	case <-time.After(10 * time.Second):
		t.Fatal("no reply posted")
	}
}

// The dialog shows a repository's command file whole, its control
// characters made visible, and Trust trusts it as shown, through the
// daemon.
func TestRepoTrustDialogTrustsTheFileAsShown(t *testing.T) {
	old := i18n.Current()
	i18n.Set("en")
	t.Cleanup(func() { i18n.Set(old) })
	m, home, posted := linkedTestModel(t)

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, ".mako", "worktree.toml")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "setup_cmd = \"make\"\x1b[8m hidden\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	m.askRepoTrust("", file, "setup_cmd was skipped")
	runPosted(t, posted)
	a := ggui.Peek(m.RepoTrust)
	if a.File == nil || string(a.File.GetContent()) != content {
		t.Fatalf("asked = %+v", a)
	}

	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Stack(repoTrustDialog(m)).Expand() }, ggui.Sz(900, 700))
	defer p.Close()
	p.Frame()
	sem := p.Semantics()
	if _, ok := sem.Find(ggui.RoleText, "setup_cmd = \"make\"\\x1b[8m hidden"); !ok {
		t.Fatal("the file's content is not shown with its escape visible")
	}
	if _, ok := sem.Find(ggui.RoleText, "SHA-256 "+repotrust.Hash([]byte(content))); !ok {
		t.Fatal("the file's hash is not shown")
	}
	trust, ok := sem.Find(ggui.RoleButton, "Trust")
	if !ok {
		t.Fatal("no Trust button")
	}
	p.Click(trust.Rect.Center())
	runPosted(t, posted)
	if ggui.Peek(m.RepoTrust).File != nil {
		t.Fatalf("dialog still open: %+v", ggui.Peek(m.RepoTrust))
	}
	if !repotrust.Open(home).Trusted(repo, ".mako/worktree.toml", []byte(content)) {
		t.Fatal("not trusted through the daemon")
	}

	// A file that changes between showing and trusting is refused.
	m.askRepoTrust("", file, "")
	runPosted(t, posted)
	if ggui.Peek(m.RepoTrust).File != nil {
		t.Fatal("asked about a file already trusted")
	}
	if err := os.WriteFile(file, []byte("setup_cmd = \"other\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.askRepoTrust("", file, "")
	runPosted(t, posted)
	if err := os.WriteFile(file, []byte("setup_cmd = \"evil\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.trustRepoFile()
	runPosted(t, posted)
	if a := ggui.Peek(m.RepoTrust); a.File == nil || a.Err == "" || a.Busy {
		t.Fatalf("a changed file was trusted: %+v", a)
	}
}
