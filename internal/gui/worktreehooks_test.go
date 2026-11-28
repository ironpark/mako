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
)

// A session whose worktree was made without its repository's setup_cmd
// is told of once, with a Review that opens the file to trust it.
func TestSkippedHooksToastOpensTheTrustDialog(t *testing.T) {
	old := i18n.Current()
	i18n.Set("en")
	t.Cleanup(func() { i18n.Set(old) })
	m, _, posted := linkedTestModel(t)

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, ".mako", "worktree.toml")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("setup_cmd = \"npm ci\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Stack(m.toaster, repoTrustDialog(m)).Expand() }, ggui.Sz(900, 700))
	defer p.Close()
	m.noteSkippedHooks(&protocol.SessionInfo{Id: "plain"})
	info := &protocol.SessionInfo{Id: "s1", Worktree: &protocol.Worktree{Repo: repo, Path: repo + "-wt", SkippedHooks: file}}
	m.noteSkippedHooks(info)
	m.noteSkippedHooks(info) // a teammate's, of the same file
	if n := m.toaster.Len(); n != 1 {
		t.Fatalf("%d toasts, want 1", n)
	}
	for range 20 {
		p.Advance(50 * time.Millisecond) // the toast slides in
	}
	if _, ok := p.FindRole(ggui.RoleButton, "Review…"); !ok {
		t.Fatal("no Review button on the toast")
	}
	p.Tap("Review…")
	runPosted(t, posted)
	a := ggui.Peek(m.RepoTrust)
	if a.File == nil || string(a.File.GetContent()) != "setup_cmd = \"npm ci\"\n" || !strings.Contains(a.Note, "did not run the setup_cmd") || a.Path != file {
		t.Fatalf("asked = %+v", a)
	}
}
