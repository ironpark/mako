package gui

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
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

// commandFile is a folder taken for a repository, by its .git folder, with
// the command file .mako/commands.toml reading content.
func commandFile(t *testing.T, content string) (repo, file string) {
	t.Helper()
	repo = t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(repo, ".mako", "commands.toml")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo, file
}

func inEnglish(t *testing.T) {
	old := i18n.Current()
	i18n.Set("en")
	t.Cleanup(func() { i18n.Set(old) })
}

// The dialog shows a repository's command file whole, its control
// characters made visible, and Trust trusts it as shown, through the
// daemon.
func TestRepoTrustDialogTrustsTheFileAsShown(t *testing.T) {
	inEnglish(t)
	m, home, posted := linkedTestModel(t)
	content := "cmd = \"make\"\x1b[8m hidden\n"
	repo, file := commandFile(t, content)

	m.askRepoTrust("", file, "the commands were skipped")
	runPosted(t, posted)
	a := ggui.Peek(m.RepoTrust)
	if a.File == nil || string(a.File.GetContent()) != content {
		t.Fatalf("asked = %+v", a)
	}

	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Stack(repoTrustDialog(m)).Expand() }, ggui.Sz(900, 700))
	defer p.Close()
	p.Frame()
	sem := p.Semantics()
	if _, ok := sem.Find(ggui.RoleText, "cmd = \"make\"\\x1b[8m hidden"); !ok {
		t.Fatal("the file's content is not shown with its escape visible")
	}
	if _, ok := sem.Find(ggui.RoleText, "SHA-256 "+repotrust.Hash([]byte(content))); !ok {
		t.Fatal("the file's hash is not shown")
	}
	real, _ := filepath.EvalSymlinks(repo)
	if _, ok := sem.Find(ggui.RoleText, filepath.Join(real, ".mako", "commands.toml")); !ok {
		t.Fatal("the file is not named where the node read it")
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
	if !repotrust.Open(home).Trusted(repo, ".mako/commands.toml", []byte(content)) {
		t.Fatal("not trusted through the daemon")
	}
	m.askRepoTrust("", file, "")
	runPosted(t, posted)
	if ggui.Peek(m.RepoTrust).File != nil {
		t.Fatal("asked about a file already trusted")
	}
}

// A file that changes between showing and trusting is refused, and the
// dialog offers to read it again: Trust waits until it is, and then trusts
// it as it was read again.
func TestRepoTrustDialogReloadsAFileThatChanged(t *testing.T) {
	inEnglish(t)
	m, home, posted := linkedTestModel(t)
	repo, file := commandFile(t, "cmd = \"other\"\n")
	m.askRepoTrust("", file, "")
	runPosted(t, posted)
	if err := os.WriteFile(file, []byte("cmd = \"evil\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.trustRepoFile()
	runPosted(t, posted)
	if a := ggui.Peek(m.RepoTrust); a.File == nil || !a.Changed || a.Busy {
		t.Fatalf("a changed file was trusted: %+v", a)
	}
	if repotrust.Open(home).Trusted(repo, ".mako/commands.toml", []byte("cmd = \"evil\"\n")) {
		t.Fatal("trusted what was not shown")
	}

	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Stack(repoTrustDialog(m)).Expand() }, ggui.Sz(900, 700))
	defer p.Close()
	sem := p.Semantics()
	if trust, ok := sem.Find(ggui.RoleButton, "Trust"); !ok || !trust.Disabled {
		t.Fatalf("Trust is not held back for a file that changed: %+v", trust)
	}
	reload, ok := sem.Find(ggui.RoleButton, "Reload")
	if !ok {
		t.Fatal("no Reload for a file that changed")
	}
	p.Click(reload.Rect.Center())
	runPosted(t, posted)
	if a := ggui.Peek(m.RepoTrust); a.Changed || string(a.File.GetContent()) != "cmd = \"evil\"\n" {
		t.Fatalf("not read again: %+v", a)
	}
	sem = p.Semantics()
	if _, ok := sem.Find(ggui.RoleText, "cmd = \"evil\""); !ok {
		t.Fatal("the file as it reads now is not shown")
	}
	if _, ok := sem.Find(ggui.RoleButton, "Reload"); ok {
		t.Fatal("Reload is still offered once read again")
	}
	trust, ok := sem.Find(ggui.RoleButton, "Trust")
	if !ok || trust.Disabled {
		t.Fatalf("Trust held back once read again: %+v", trust)
	}
	p.Click(trust.Rect.Center())
	runPosted(t, posted)
	if !repotrust.Open(home).Trusted(repo, ".mako/commands.toml", []byte("cmd = \"evil\"\n")) {
		t.Fatal("not trusted once read again")
	}
}

// While a trust is under way the dialog waits for its answer: neither
// Cancel nor Escape closes it, as both do otherwise.
func TestRepoTrustDialogWaitsForItsAnswer(t *testing.T) {
	inEnglish(t)
	m, home, posted := linkedTestModel(t)
	repo, file := commandFile(t, "cmd = \"make\"\n")
	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Stack(repoTrustDialog(m)).Expand() }, ggui.Sz(900, 700))
	defer p.Close()

	m.askRepoTrust("", file, "")
	runPosted(t, posted)
	p.Frame()
	p.Key("escape")
	if ggui.Peek(m.RepoTrust).File != nil {
		t.Fatal("Escape does not close the dialog")
	}

	m.askRepoTrust("", file, "")
	runPosted(t, posted)
	p.Frame()
	m.trustRepoFile() // its answer not yet taken
	sem := p.Semantics()
	cancel, ok := sem.Find(ggui.RoleButton, "Cancel")
	if !ok || !cancel.Disabled {
		t.Fatalf("Cancel is not held back while busy: %+v", cancel)
	}
	p.Click(cancel.Rect.Center())
	p.Key("escape")
	p.Frame()
	if a := ggui.Peek(m.RepoTrust); a.File == nil || !a.Busy {
		t.Fatalf("closed while its answer was awaited: %+v", a)
	}
	runPosted(t, posted)
	if a := ggui.Peek(m.RepoTrust); a.File != nil {
		t.Fatalf("still open once answered: %+v", a)
	}
	if !repotrust.Open(home).Trusted(repo, ".mako/commands.toml", []byte("cmd = \"make\"\n")) {
		t.Fatal("not trusted")
	}
}

// A Space window asks in a dialog of its own, and tells of the answer over
// itself; the main window shows none of it. A Space window closed while
// an answer is awaited hands the dialog to the main window.
func TestRepoTrustAsksInTheWindowThatAsks(t *testing.T) {
	inEnglish(t)
	m, _, posted := linkedTestModel(t)
	_, file := commandFile(t, "cmd = \"make\"\n")
	sw := m.openSpaceWindow(allSpaceID)
	if sw == nil || sw.toaster == nil {
		t.Fatalf("space window = %+v", sw)
	}
	space := ggui.ProbeBuilder(func() ggui.Widget { return spaceWindowView(m, sw) }, ggui.Sz(1000, 700))
	defer space.Close()
	main := ggui.ProbeBuilder(func() ggui.Widget { return widget.Stack(repoTrustDialog(m)).Expand() }, ggui.Sz(900, 700))
	defer main.Close()

	m.askRepoTrustIn(sw.spaceID, "", file, "")
	runPosted(t, posted)
	if _, ok := main.Semantics().Find(ggui.RoleButton, "Trust"); ok {
		t.Fatal("the main window shows a Space window's dialog")
	}
	trust, ok := space.Semantics().Find(ggui.RoleButton, "Trust")
	if !ok {
		t.Fatal("the Space window does not show its dialog")
	}
	space.Click(trust.Rect.Center())
	runPosted(t, posted)
	if sw.toaster.Len() != 1 || m.toaster.Len() != 0 {
		t.Fatalf("told of the trust in the space window %d times, in the main %d", sw.toaster.Len(), m.toaster.Len())
	}

	if err := os.WriteFile(file, []byte("cmd = \"other\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.askRepoTrustIn(sw.spaceID, "", file, "")
	runPosted(t, posted)
	m.trustRepoFile()
	m.closeSpaceWindow(sw.spaceID)
	if a := ggui.Peek(m.RepoTrust); a.File == nil || a.Owner != "" {
		t.Fatalf("the dialog awaiting its answer went with its window: %+v", a)
	}
	if _, ok := main.Semantics().Find(ggui.RoleButton, "Trust"); !ok {
		t.Fatal("the main window does not take the dialog over")
	}
	runPosted(t, posted)
	if m.toaster.Len() != 1 {
		t.Fatalf("told of the trust in the main window %d times", m.toaster.Len())
	}
}

// The file is made printable and laid out once for each content, not at
// each change of the dialog's state.
func TestShownFileIsLaidOutOncePerContent(t *testing.T) {
	var s shownFile
	a := s.of([]byte("cmd = 1\n"))
	if b := s.of([]byte("cmd = 1\n")); b != a {
		t.Fatal("the same content laid out again")
	}
	if c := s.of([]byte("cmd = 2\n")); c == a {
		t.Fatal("another content not laid out")
	}
}

// A toast goes over the window the user is in: the main window while no
// Space window has the focus.
func TestToastGoesOverTheWindowTheUserIsIn(t *testing.T) {
	m := newModel(config.Default(), "")
	sw := m.openSpaceWindow(allSpaceID)
	if sw == nil {
		t.Fatal("no space window")
	}
	if owner := m.focusedOwner(); owner != "" {
		t.Fatalf("a Space window without the focus is taken for the one in use: %q", owner)
	}
	m.toast(ui.Toast("note", ""))
	if m.toaster.Len() != 1 || sw.toaster.Len() != 0 {
		t.Fatalf("toasts: main %d, space window %d", m.toaster.Len(), sw.toaster.Len())
	}
}
