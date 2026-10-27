package gui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

// gitProject is a repository with a merged branch and changes not yet
// committed.
func gitProject(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root, "-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "T")
	git("config", "user.email", "t@example.com")
	git("config", "commit.gpgsign", "false")
	write("main.go", "package main\n")
	git("add", "-A")
	git("commit", "-q", "-m", "Start")
	git("switch", "-q", "-c", "feature")
	write("feature.go", "package main\n\nfunc feature() {}\n")
	git("add", "-A")
	git("commit", "-q", "-m", "Add the feature")
	git("switch", "-q", "main")
	write("README.md", "# project\n")
	git("add", "-A")
	git("commit", "-q", "-m", "Write a readme")
	git("merge", "-q", "--no-ff", "-m", "Merge the feature", "feature")
	write("main.go", "package main\n\nfunc main() {}\n")
	write("notes.txt", "todo\n")
	return root, git
}

func TestGitPanelsCommitAndShowHistory(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
	project, git := gitProject(t)
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
	if err := savePrefs(prefs{DaemonMode: "embedded", Language: "en"}.normalized()); err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg, "")
	ctx, cancel := context.WithCancel(t.Context())
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1100, 760))
	p.Setup(func() { setup(m); m.start(ctx, p.Post) })
	t.Cleanup(func() { cancel(); m.shutdown(); p.Close() })
	probeUntil(t, p, "daemon", func() bool { return ggui.Peek(m.Conn).OK })
	m.launch("shell", "", project)
	probeUntil(t, p, "session", func() bool { return ggui.Untrack(m.filesRoot).Path != "" })

	// Files: a file made through the context menu's dialog.
	root := ggui.Untrack(m.filesRoot)
	m.askFile(protocol.FileCreate, fileItem{Key: dirKey(root.Node, root.Path), Node: root.Node, Path: root.Path, Name: "project", Dir: true})
	m.FileName.Set("made.txt")
	p.Frame()
	m.submitFile()
	probeUntil(t, p, "the new file", func() bool {
		_, err := os.Stat(filepath.Join(project, "made.txt"))
		return err == nil && !ggui.Peek(m.FileNameOpen)
	})

	m.DetailTab.Set(gitTab)
	probeUntil(t, p, "changes", func() bool { return len(ggui.Peek(m.Git).Changes.GetFiles()) == 3 })

	// Commit main.go and made.txt, leaving notes.txt out.
	p.Tap(Tf("Commit %s", "notes.txt"))
	p.Frame()
	if !ggui.Peek(m.GitSkip)["notes.txt"] || len(ggui.Peek(m.Diffs)) != 0 {
		t.Fatalf("the box: skipped %v, diffs %v", ggui.Peek(m.GitSkip), ggui.Peek(m.Diffs))
	}
	m.GitMessage.Set("Fill in main")
	p.Frame()
	p.Tap(T("Commit 2 files"))
	probeUntil(t, p, "the commit", func() bool { return ggui.Peek(m.GitBusy) == "" && ggui.Peek(m.GitMessage) == "" })
	if n := ggui.Peek(m.GitNote); n.Err {
		t.Fatalf("commit failed: %s", n.Text)
	}
	if st := strings.TrimSpace(git("status", "--porcelain")); st != "?? notes.txt" {
		t.Fatalf("after the commit: %q", st)
	}
	if log := git("log", "-1", "--format=%s"); strings.TrimSpace(log) != "Fill in main" {
		t.Fatalf("last commit %q", log)
	}

	probeUntil(t, p, "history", func() bool { return len(ggui.Peek(m.GitHistory).History.GetCommits()) == 5 })
	p.Frame()
	h := ggui.Peek(m.GitHistory).History
	if h.GetCurrent() != "main" || h.GetCommits()[0].GetSubject() != "Fill in main" || len(h.GetCommits()[1].GetParents()) != 2 {
		t.Fatalf("history %+v", h)
	}
	m.openCommit(h.GetCommits()[1].GetHash())
	probeUntil(t, p, "the merge", func() bool { c := ggui.Peek(m.GitCommit); return !c.Loading && c.Info.GetCommit().GetHash() != "" })
	m.openCommitFile(ggui.Peek(m.GitCommit).Info.GetFiles()[0])
	diff := ggui.Peek(m.Selected)
	probeUntil(t, p, "its diff", func() bool { d, ok := ggui.Peek(m.Diffs)[diff]; return ok && !d.Loading })
	if d := ggui.Peek(m.Diffs)[diff]; d.Err != "" || len(d.Diff.GetLines()) == 0 || d.Commit != h.GetCommits()[1].GetHash() {
		t.Fatalf("commit diff %+v", d)
	}
	// The project's panels stay on while the diff's tab shows.
	if r := ggui.Untrack(m.filesRoot); r.Path != project {
		t.Fatalf("panels show %+v", r)
	}
	m.askKill(diff)
	if _, ok := ggui.Peek(m.Diffs)[diff]; ok || slices.Contains(ggui.Peek(m.Tabs), diff) {
		t.Fatal("the diff's tab stayed open")
	}

	// A new branch from the header's menu, switched to.
	m.askBranch("")
	m.GitAskText.Set("next")
	m.submitAsk()
	probeUntil(t, p, "the branch", func() bool { return ggui.Peek(m.GitHistory).History.GetCurrent() == "next" })

	// notes.txt stashed under a message, then popped back.
	m.askStash()
	m.GitAskText.Set("notes for later")
	m.submitAsk()
	probeUntil(t, p, "the stash", func() bool {
		c := ggui.Peek(m.Git).Changes
		return len(c.GetStashes()) == 1 && len(c.GetFiles()) == 0
	})
	m.gitRun(&protocol.GitRun{Op: protocol.GitOpStashPop, Stash: 0}, "", nil)
	probeUntil(t, p, "the pop", func() bool {
		c := ggui.Peek(m.Git).Changes
		return len(c.GetStashes()) == 0 && len(c.GetFiles()) == 1
	})
	// Discarding asks first, then removes the new file.
	m.discardFiles([]string{"notes.txt"}, "notes.txt")
	if ggui.Peek(m.GitConfirm).Title == "" {
		t.Fatal("discarding did not ask")
	}
	ggui.Peek(m.GitConfirm).Run()
	probeUntil(t, p, "the discard", func() bool { return len(ggui.Peek(m.Git).Changes.GetFiles()) == 0 })
	if _, err := os.Stat(filepath.Join(project, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("notes.txt is still there")
	}
}
