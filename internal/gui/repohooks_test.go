package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/repotrust"
)

// A session the window starts in a repository whose .mako/hooks.toml is
// not trusted is told of once, with a Review that opens the file to trust
// it; one trusted, or a repository without the file, is not.
func TestUntrustedRepoHooksToastOpensTheTrustDialog(t *testing.T) {
	old := i18n.Current()
	i18n.Set("en")
	t.Cleanup(func() { i18n.Set(old) })
	m, home, posted := linkedTestModel(t)

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, ".mako", "hooks.toml")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "[[hook]]\nevent = \"on_turn_done\"\ncommand = \"make lint\"\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Stack(m.toaster, repoTrustDialog(m)).Expand() }, ggui.Sz(900, 700))
	defer p.Close()
	m.noteRepoHooks(&protocol.SessionInfo{Id: "plain", Project: t.TempDir()}) // no repository: nothing asked
	info := &protocol.SessionInfo{Id: "s1", Repo: repo, Project: filepath.Join(repo, "sub")}
	m.noteRepoHooks(info)
	runPosted(t, posted)
	m.noteRepoHooks(info) // another session of it, within the minute
	if n := m.toaster.Len(); n != 1 {
		t.Fatalf("%d toasts, want 1", n)
	}
	for range 20 {
		p.Advance(50 * time.Millisecond) // the toast slides in
	}
	if _, ok := p.FindRole(ggui.RoleButton, "Review hooks…"); !ok {
		t.Fatal("no Review button on the toast")
	}
	p.Tap("Review hooks…")
	runPosted(t, posted)
	a := ggui.Peek(m.RepoTrust)
	if a.File == nil || string(a.File.GetContent()) != content || !strings.Contains(a.Note, "did not run the hooks") || a.Path != file {
		t.Fatalf("asked = %+v", a)
	}

	// Trusted, a repository's next session is not told of it.
	answered := m.toaster.Len()
	if _, err := repotrust.Open(home).Trust(repo, ".mako/hooks.toml", repotrust.Hash([]byte(content))); err != nil {
		t.Fatal(err)
	}
	m.repoHooksNoted = nil
	m.noteRepoHooks(info)
	runPosted(t, posted)
	if n := m.toaster.Len(); n != answered {
		t.Fatalf("%d toasts after trusting, want %d", n, answered)
	}

	// Nor one of a repository with no hooks file.
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	m.noteRepoHooks(&protocol.SessionInfo{Id: "s2", Repo: other, Project: other})
	runPosted(t, posted)
	if n := m.toaster.Len(); n != answered {
		t.Fatalf("%d toasts for a repository without hooks, want %d", n, answered)
	}
}
