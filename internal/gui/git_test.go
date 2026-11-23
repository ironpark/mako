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

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
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

func findUiNode(n *protocol.UiNode, pred func(*protocol.UiNode) bool) *protocol.UiNode {
	if n == nil {
		return nil
	}
	if pred(n) {
		return n
	}
	for _, c := range n.GetChildren() {
		if got := findUiNode(c, pred); got != nil {
			return got
		}
	}
	return nil
}

func findUiNodeKindText(n *protocol.UiNode, kind, text string) *protocol.UiNode {
	return findUiNode(n, func(x *protocol.UiNode) bool {
		return x.GetKind() == kind && x.GetText() == text
	})
}

func TestGitPanelsCommitAndShowHistory(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	project, git := gitProject(t)
	cfg := config.Default()
	cfg.P2P.Listen = "127.0.0.1:0"
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
	m.files.Ops.Name.Set("made.txt")
	p.Frame()
	m.submitFile()
	probeUntil(t, p, "the new file", func() bool {
		_, err := os.Stat(filepath.Join(project, "made.txt"))
		return err == nil && !ggui.Peek(m.files.Ops.NameOpen)
	})

	// Switch to the plugin-backed Git panel and wait for changes + history DAG.
	m.DetailTab.Set(gitTab)
	gitScope := func() string {
		return ggui.Untrack(func() string { return m.resolveActiveScope(protocol.ScopeFolder) })
	}
	gitTree := func() *protocol.UiNode {
		return m.blockTreeFor(gitPluginBlock, gitScope())
	}
	probeUntil(t, p, "changes", func() bool {
		tr := gitTree()
		return findUiNodeKindText(tr, "tree_item", "main.go") != nil &&
			findUiNodeKindText(tr, "tree_item", "made.txt") != nil &&
			findUiNodeKindText(tr, "tree_item", "notes.txt") != nil &&
			findUiNodeSuffix(tr, "history-dag") != nil
	})
	p.Frame()

	// Tap a changed file in the tree to verify opening a worktree diff tab via intent.
	p.Tap("main.go")
	probeUntil(t, p, "worktree diff", func() bool {
		d, ok := ggui.Peek(m.Diffs)[ggui.Peek(m.Selected)]
		return ok && !d.Loading && len(d.Diff.GetLines()) > 0
	})
	wtDiff := ggui.Peek(m.Selected)
	if l := ggui.Peek(m.Diffs)[wtDiff].Diff.GetLines()[0]; l.GetOp() != "@" {
		t.Fatalf("worktree diff starts %+v, want hunk header", l)
	}
	m.askKill(wtDiff)
	p.Frame()

	// Commit main.go and made.txt, leaving notes.txt out.
	p.Tap("Commit notes.txt")
	probeUntil(t, p, "notes.txt unchecked", func() bool {
		tr := gitTree()
		notes := findUiNodeKindText(tr, "tree_item", "notes.txt")
		btn := findUiNodeSuffix(tr, "commit-btn")
		return notes != nil && notes.GetCheck() == "off" && btn != nil && btn.GetText() == "Commit 2 files"
	})
	if len(ggui.Peek(m.Diffs)) != 0 {
		t.Fatalf("toggling checkbox opened a diff: %v", ggui.Peek(m.Diffs))
	}

	// Submit commit via blockEvent with the commit message form value.
	tr := gitTree()
	msgNode := findUiNodeSuffix(tr, "commit-msg")
	btnNode := findUiNodeSuffix(tr, "commit-btn")
	if msgNode == nil || btnNode == nil {
		t.Fatalf("missing commit controls: msg=%v btn=%v", msgNode, btnNode)
	}
	m.blockEvent(gitPluginBlock, gitScope(), btnNode.GetId(), protocol.UiTap, "", map[string]string{
		msgNode.GetId(): "Fill in main",
	})
	probeUntil(t, p, "the commit in history", func() bool {
		dag := findUiNodeSuffix(gitTree(), "history-dag")
		return dag != nil && len(dag.GetRows()) == 5 && dag.GetRows()[0].GetText() == "Fill in main"
	})
	if st := strings.TrimSpace(git("status", "--porcelain")); st != "?? notes.txt" {
		t.Fatalf("after the commit: %q", st)
	}
	if log := git("log", "-1", "--format=%s"); strings.TrimSpace(log) != "Fill in main" {
		t.Fatalf("last commit %q", log)
	}

	// Verify DAG rows (HEAD strong dot, merge commit with 2 parents).
	p.Frame()
	dag := findUiNodeSuffix(gitTree(), "history-dag")
	if !dag.GetRows()[0].GetStrong() {
		t.Fatalf("HEAD row should have strong=true: %+v", dag.GetRows()[0])
	}
	mergeRow := dag.GetRows()[1]
	if mergeRow.GetText() != "Merge the feature" || len(mergeRow.GetParents()) != 2 {
		t.Fatalf("expected merge commit row with 2 parents, got %+v", mergeRow)
	}

	// Tap the merge commit row in the DAG to open commit detail, then open its file diff.
	p.Tap("Merge the feature")
	probeUntil(t, p, "the merge detail", func() bool {
		tr := gitTree()
		return findUiNodeSuffix(tr, "commit-detail") != nil && findUiNodeKindText(tr, "button", "feature.go") != nil
	})
	p.Frame()
	if btn := findUiNodeKindText(gitTree(), "button", "feature.go"); btn != nil {
		m.blockEvent(gitPluginBlock, gitScope(), btn.GetId(), protocol.UiTap, "", nil)
	}
	probeUntil(t, p, "commit diff", func() bool {
		d, ok := ggui.Peek(m.Diffs)[ggui.Peek(m.Selected)]
		return ok && !d.Loading
	})
	diff := ggui.Peek(m.Selected)
	if d := ggui.Peek(m.Diffs)[diff]; d.Err != "" || len(d.Diff.GetLines()) == 0 || d.Commit != mergeRow.GetId() {
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

	// Create a new branch "next" via the plugin dialog, then switch back to "main".
	newBranchItem := findUiNodeSuffix(gitTree(), "br-new")
	if newBranchItem == nil {
		t.Fatal("missing New branch… menu item")
	}
	m.blockEvent(gitPluginBlock, gitScope(), newBranchItem.GetId(), protocol.UiTap, "", nil)
	probeUntil(t, p, "branch dialog", func() bool {
		dlg := findUiNodeSuffix(gitTree(), "git-dialog")
		return dlg != nil && dlg.GetText() == "New branch"
	})
	tr = gitTree()
	m.blockEvent(gitPluginBlock, gitScope(), findUiNodeSuffix(tr, "dlg-submit").GetId(), protocol.UiTap, "", map[string]string{
		findUiNodeSuffix(tr, "dialog-input").GetId(): "next",
	})
	probeUntil(t, p, "branch next", func() bool {
		bm := findUiNodeSuffix(gitTree(), "branch-menu")
		return bm != nil && bm.GetText() == "next"
	})
	if cur := strings.TrimSpace(git("rev-parse", "--abbrev-ref", "HEAD")); cur != "next" {
		t.Fatalf("HEAD branch = %q, want next", cur)
	}
	mainItem := findUiNodeKindText(gitTree(), "menu_item", "main")
	if mainItem == nil {
		t.Fatal("missing main branch menu_item")
	}
	m.blockEvent(gitPluginBlock, gitScope(), mainItem.GetId(), protocol.UiTap, "", nil)
	probeUntil(t, p, "back to main", func() bool {
		bm := findUiNodeSuffix(gitTree(), "branch-menu")
		return bm != nil && bm.GetText() == "main"
	})

	// Stash notes.txt under a message, expand Stashes, then pop it back.
	m.blockEvent(gitPluginBlock, gitScope(), findUiNodeSuffix(gitTree(), "op-stash").GetId(), protocol.UiTap, "", nil)
	probeUntil(t, p, "stash dialog", func() bool {
		dlg := findUiNodeSuffix(gitTree(), "git-dialog")
		return dlg != nil && dlg.GetText() == "Stash changes"
	})
	tr = gitTree()
	m.blockEvent(gitPluginBlock, gitScope(), findUiNodeSuffix(tr, "dlg-submit").GetId(), protocol.UiTap, "", map[string]string{
		findUiNodeSuffix(tr, "dialog-input").GetId(): "notes for later",
	})
	probeUntil(t, p, "the stash", func() bool {
		return findUiNodeKindText(gitTree(), "text", "No changes.") != nil
	})
	if st := strings.TrimSpace(git("status", "--porcelain")); st != "" {
		t.Fatalf("expected clean tree after stash, got %q", st)
	}
	m.blockEvent(gitPluginBlock, gitScope(), findUiNodeSuffix(gitTree(), "sec-stashes-toggle").GetId(), protocol.UiTap, "", nil)
	probeUntil(t, p, "stashes expanded", func() bool {
		return findUiNodeKindText(gitTree(), "button", "Pop") != nil
	})
	m.blockEvent(gitPluginBlock, gitScope(), findUiNodeKindText(gitTree(), "button", "Pop").GetId(), protocol.UiTap, "", nil)
	probeUntil(t, p, "the pop", func() bool {
		return findUiNodeKindText(gitTree(), "tree_item", "notes.txt") != nil
	})

	// Discarding opens a danger confirm dialog first, then removes notes.txt.
	m.blockEvent(gitPluginBlock, gitScope(), findUiNodeSuffix(gitTree(), "op-discard-all").GetId(), protocol.UiTap, "", nil)
	probeUntil(t, p, "discard dialog", func() bool {
		dlg := findUiNodeSuffix(gitTree(), "git-dialog")
		return dlg != nil && dlg.GetVariant() == "danger"
	})
	if _, err := os.Stat(filepath.Join(project, "notes.txt")); err != nil {
		t.Fatalf("notes.txt disappeared before confirm: %v", err)
	}
	p.Frame()
	p.Tap("Discard")
	probeUntil(t, p, "the discard", func() bool {
		tr := gitTree()
		return findUiNodeSuffix(tr, "git-dialog") == nil && findUiNodeKindText(tr, "text", "No changes.") != nil
	})
	if _, err := os.Stat(filepath.Join(project, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("notes.txt is still there")
	}
}

// Switching between two repositories updates the plugin-backed Git panel to
// the newly selected repository's history.
func TestGitPanelFollowsSelectedRepository(t *testing.T) {
	m, p := daemonApp(t)
	first, _ := gitProject(t)
	second, git2 := gitProject(t)
	git2("commit", "-q", "--allow-empty", "-m", "One more")

	m.DetailTab.Set(gitTab)
	m.launch("shell", "", first)
	probeUntil(t, p, "first project's history", func() bool {
		dag := findUiNodeSuffix(m.blockTreeFor(gitPluginBlock), "history-dag")
		return dag != nil && len(dag.GetRows()) == 4 && dag.GetRows()[0].GetText() == "Merge the feature"
	})

	m.launch("shell", "", second)
	probeUntil(t, p, "second project's history", func() bool {
		scope := ggui.Untrack(func() string { return m.resolveActiveScope(protocol.ScopeFolder) })
		dag := findUiNodeSuffix(m.blockTreeFor(gitPluginBlock, scope), "history-dag")
		return dag != nil && len(dag.GetRows()) == 5 && dag.GetRows()[0].GetText() == "One more"
	})
}
