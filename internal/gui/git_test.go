package gui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

	m.DetailTab.Set(changesTab)
	probeUntil(t, p, "changes", func() bool { return len(ggui.Peek(m.Git).Changes.Files) == 3 })

	// Commit main.go and made.txt, leaving notes.txt out.
	p.Tap(Tf("Commit %s", "notes.txt"))
	p.Frame()
	if !ggui.Peek(m.GitSkip)["notes.txt"] || ggui.Peek(m.GitDiff).Key != "" {
		t.Fatalf("the box: skipped %v, diff %q", ggui.Peek(m.GitSkip), ggui.Peek(m.GitDiff).Key)
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

	m.DetailTab.Set(historyTab)
	probeUntil(t, p, "history", func() bool { return len(ggui.Peek(m.GitHistory).History.Commits) == 5 })
	p.Frame()
	h := ggui.Peek(m.GitHistory).History
	if h.Current != "main" || h.Commits[0].Subject != "Fill in main" || len(h.Commits[1].Parents) != 2 {
		t.Fatalf("history %+v", h)
	}
	m.openCommit(h.Commits[1].Hash)
	probeUntil(t, p, "the merge", func() bool { c := ggui.Peek(m.GitCommit); return !c.Loading && c.Info.Commit.Hash != "" })
	m.openCommitFile(ggui.Peek(m.GitCommit).Info.Files[0])
	probeUntil(t, p, "its diff", func() bool { return !ggui.Peek(m.GitCommit).Reading })
	if d := ggui.Peek(m.GitCommit); d.DiffErr != "" || len(d.Diff.Lines) == 0 {
		t.Fatalf("commit diff %+v", d)
	}

	// A new branch from the header's menu, switched to.
	m.BranchName.Set("next")
	m.submitBranch()
	probeUntil(t, p, "the branch", func() bool { return ggui.Peek(m.GitHistory).History.Current == "next" })
}
