package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/plugin/builtin"
	"github.com/ironpark/mako/internal/protocol"
)

const gitBlockName = "plugin:mako-git/panel"

func TestBuiltinFSContainsMakoGit(t *testing.T) {
	required := []string{
		"mako-git/plugin.toml",
		"mako-git/main.js",
		"mako-git/lib/i18n.js",
		"mako-git/lib/state.js",
		"mako-git/lib/data.js",
		"mako-git/lib/tree.js",
		"mako-git/lib/actions.js",
		"mako-git/lib/view/toolbar.js",
		"mako-git/lib/view/changes.js",
		"mako-git/lib/view/commitbox.js",
		"mako-git/lib/view/branches.js",
		"mako-git/lib/view/stashes.js",
		"mako-git/lib/view/history.js",
		"mako-git/lib/view/lines.js",
		"mako-git/lib/view/dialogs.js",
		"mako-git/lib/view/layout.js",
	}
	for _, p := range required {
		data, err := fs.ReadFile(builtin.FS, p)
		if err != nil {
			t.Fatalf("missing embedded file %s: %v", p, err)
		}
		if len(data) == 0 {
			t.Fatalf("embedded file %s is empty", p)
		}
	}
	manifest, _ := fs.ReadFile(builtin.FS, "mako-git/plugin.toml")
	for _, want := range []string{`id = "mako-git"`, `publisher = "mako"`, `scope = "folder"`, `chrome = "toolbar"`, `hidden = true`} {
		if !strings.Contains(string(manifest), want) {
			t.Fatalf("plugin.toml missing %s:\n%s", want, manifest)
		}
	}
}

type testRig struct {
	t      *testing.T
	m      *plugin.Manager
	cancel context.CancelFunc
	done   chan struct{}
	logs   *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func startBuiltinRig(t *testing.T) *testRig {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "plugins")
	data := filepath.Join(base, "plugin-data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	m := plugin.New(plugin.Config{
		Dir:     dir,
		DataDir: data,
		Poll:    50 * time.Millisecond,
		Log:     slog.New(slog.NewTextHandler(io.MultiWriter(logs), &slog.HandlerOptions{Level: slog.LevelDebug})),
		Limits: plugin.Limits{
			CPU:     10 * time.Second,
			Wall:    20 * time.Second,
			Memory:  32 << 20,
			Updates: 20 * time.Millisecond,
		},
		Bundled: builtin.FS,
		Git:     plugin.LocalGit{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		if t.Failed() {
			t.Logf("plugin manager log:\n%s", logs.String())
		}
	})
	r := &testRig{t: t, m: m, cancel: cancel, done: done, logs: logs}
	r.waitPlugin("mako-git", func(p *protocol.PluginInfo) bool {
		return p.GetBundled() && (p.GetState() == protocol.PluginReady || p.GetState() == protocol.PluginRunning)
	})
	return r
}

func (r *testRig) waitPlugin(id string, cond func(*protocol.PluginInfo) bool) *protocol.PluginInfo {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last *protocol.PluginInfo
	for time.Now().Before(deadline) {
		for _, p := range r.m.List() {
			if p.GetId() == id {
				last = p
				if cond(p) {
					return p
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	r.t.Fatalf("plugin %s never matched condition; last=%v", id, last)
	return nil
}

type blockWatcher struct {
	t       *testing.T
	mu      sync.Mutex
	tree    *protocol.UiNode
	last    *protocol.BlockUpdate
	err     error
	cancel  context.CancelFunc
	changed chan struct{}
}

func (r *testRig) watch(scope, locale string, view plugin.View) *blockWatcher {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := &blockWatcher{t: r.t, cancel: cancel, changed: make(chan struct{}, 1)}
	go func() {
		err := r.m.WatchScopedView(ctx, gitBlockName, scope, locale, view, func(u *protocol.BlockUpdate) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.last = u
			switch {
			case u.GetTree() != nil:
				w.tree = u.GetTree()
			case len(u.GetOps()) > 0:
				next, err := protocol.ApplyUi(w.tree, u.GetOps())
				if err != nil {
					w.err = err
					return err
				}
				w.tree = next
			}
			select {
			case w.changed <- struct{}{}:
			default:
			}
			return nil
		})
		w.mu.Lock()
		if w.err == nil && err != nil && ctx.Err() == nil {
			w.err = err
		}
		w.mu.Unlock()
	}()
	r.t.Cleanup(cancel)
	return w
}

func (w *blockWatcher) until(what string, cond func(tree *protocol.UiNode, u *protocol.BlockUpdate) bool) *protocol.UiNode {
	w.t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		w.mu.Lock()
		tree, last, err := w.tree, w.last, w.err
		w.mu.Unlock()
		if err != nil {
			w.t.Fatalf("watch error while waiting for %s: %v", what, err)
		}
		if last != nil && last.GetError() != "" {
			w.t.Fatalf("block error while waiting for %s: %s", what, last.GetError())
		}
		if last != nil && tree != nil && cond(tree, last) {
			return tree
		}
		select {
		case <-w.changed:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			w.t.Fatalf("timed out waiting for %s; last=%v", what, last)
		}
	}
}

func (r *testRig) sendEvent(scope, nodeID, kind, value string, values map[string]string) *protocol.PluginDone {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done, err := r.m.Event(ctx, &protocol.BlockEvent{
		Block:  gitBlockName,
		Scope:  scope,
		Node:   nodeID,
		Kind:   kind,
		Value:  value,
		Values: values,
	})
	if err != nil {
		r.t.Fatalf("Event(%s, %s, %q) failed: %v", nodeID, kind, value, err)
	}
	return done
}

func initTestRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{
			"-C", root,
			"-c", "user.name=Tester",
			"-c", "user.email=tester@example.com",
			"-c", "commit.gpgsign=false",
		}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return string(out)
	}
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Tester")
	git("config", "user.email", "tester@example.com")
	git("config", "commit.gpgsign", "false")

	write("main.go", "package main\n")
	git("add", "-A")
	git("commit", "-q", "-m", "Start")

	git("switch", "-q", "-c", "feature")
	write("feature.go", "package main\n\nfunc feature() {}\n")
	git("add", "-A")
	git("commit", "-q", "-m", "Add feature")

	git("switch", "-q", "main")
	write("README.md", "# test\n")
	git("add", "-A")
	git("commit", "-q", "-m", "Write readme")

	git("merge", "-q", "--no-ff", "-m", "Merge feature", "feature")

	// Working tree modifications including a nested chain of single-child dirs.
	write("main.go", "package main\n\nfunc main() {}\n")
	write("notes.txt", "todo\n")
	write("app/src/gen/a.txt", "line1\nline2\n")
	write("app/src/gen/b.txt", "one\n")
	return root, git
}

func findNode(n *protocol.UiNode, pred func(*protocol.UiNode) bool) *protocol.UiNode {
	if n == nil {
		return nil
	}
	if pred(n) {
		return n
	}
	for _, c := range n.GetChildren() {
		if got := findNode(c, pred); got != nil {
			return got
		}
	}
	return nil
}

func findByKindAndText(n *protocol.UiNode, kind, text string) *protocol.UiNode {
	return findNode(n, func(x *protocol.UiNode) bool {
		return x.GetKind() == kind && x.GetText() == text
	})
}

func findByKeySuffix(n *protocol.UiNode, key string) *protocol.UiNode {
	suffix := "/" + key
	return findNode(n, func(x *protocol.UiNode) bool {
		return strings.HasSuffix(x.GetId(), suffix)
	})
}

func TestMakoGitCompactStandaloneTreePickAndDiffIntent(t *testing.T) {
	repo, _ := initTestRepo(t)
	scope := protocol.FolderScope(protocol.LocalScopeNode, repo)
	r := startBuiltinRig(t)

	// 1. Compact (tabbed) mode: vertical split, toolbar chrome, collapsed dirs, tri-state pick.
	wCompact := r.watch(scope, "en", plugin.View{Standalone: false})
	tree := wCompact.until("changes tree loaded", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetChrome() == "toolbar" && findByKindAndText(n, "tree_item", "app/src/gen") != nil
	})

	if !tree.GetFill() || len(tree.GetChildren()) == 0 {
		t.Fatalf("root must be fill=true container with toolbar child 0: %+v", tree)
	}
	tb := tree.GetChildren()[0]
	if tb.GetKind() != "row" || tb.GetVariant() != "toolbar" {
		t.Fatalf("root child 0 = (%s, %q); want (row, toolbar)", tb.GetKind(), tb.GetVariant())
	}

	mainSplit := findByKeySuffix(tree, "main-split")
	if mainSplit == nil || mainSplit.GetDirection() != "vertical" {
		t.Fatalf("compact main-split = %+v; want direction=vertical", mainSplit)
	}

	// Verify collapsed directory chain "app/src/gen" aggregates +3 -0 from a.txt (+2) and b.txt (+1).
	dirNode := findByKindAndText(tree, "tree_item", "app/src/gen")
	if dirNode.GetCheck() != "on" || !dirNode.GetChecks() {
		t.Fatalf("app/src/gen check = (%q, %v); want (on, true)", dirNode.GetCheck(), dirNode.GetChecks())
	}
	if len(dirNode.GetBadges()) < 2 || dirNode.GetBadges()[0].GetText() != "+3" || dirNode.GetBadges()[1].GetText() != "-0" {
		t.Fatalf("app/src/gen badges = %+v; want +3, -0", dirNode.GetBadges())
	}

	// Tri-state checkbox: uncheck a.txt -> dir becomes "mixed"; uncheck b.txt -> dir becomes "off".
	fileA := findByKindAndText(tree, "tree_item", "a.txt")
	fileB := findByKindAndText(tree, "tree_item", "b.txt")
	if fileA == nil || fileB == nil {
		t.Fatalf("missing child files under app/src/gen: a=%v b=%v", fileA, fileB)
	}
	r.sendEvent(scope, fileA.GetId(), "check", "off", nil)
	_ = wCompact.until("mixed dir check", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		d := findByKindAndText(n, "tree_item", "app/src/gen")
		return d != nil && d.GetCheck() == "mixed"
	})
	r.sendEvent(scope, fileB.GetId(), "check", "off", nil)
	tree = wCompact.until("off dir check", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		d := findByKindAndText(n, "tree_item", "app/src/gen")
		return d != nil && d.GetCheck() == "off"
	})
	// Re-check folder -> both children return to "on".
	dirNode = findByKindAndText(tree, "tree_item", "app/src/gen")
	r.sendEvent(scope, dirNode.GetId(), "check", "on", nil)
	tree = wCompact.until("restored dir check", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		a := findByKindAndText(n, "tree_item", "a.txt")
		b := findByKindAndText(n, "tree_item", "b.txt")
		return a != nil && b != nil && a.GetCheck() == "on" && b.GetCheck() == "on"
	})

	// Tapping a file tree_item emits an openDiff window intent (preview=true).
	mainFile := findByKindAndText(tree, "tree_item", "main.go")
	if mainFile == nil {
		t.Fatal("missing main.go tree_item")
	}
	done := r.sendEvent(scope, mainFile.GetId(), protocol.UiTap, "", nil)
	if len(done.GetOpen()) != 1 {
		t.Fatalf("openDiff intents = %+v; want 1", done.GetOpen())
	}
	intent := done.GetOpen()[0]
	if intent.GetKind() != "diff" || intent.GetPath() != "main.go" || !intent.GetPreview() || intent.GetCommit() != "" {
		t.Fatalf("unexpected openDiff intent: %+v", intent)
	}

	// 2. Standalone mode: closing compact watcher and opening standalone flips main-split to "auto".
	wCompact.cancel()
	wStandalone := r.watch(scope, "en", plugin.View{Standalone: true})
	tree = wStandalone.until("standalone auto split", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		sp := findByKeySuffix(n, "main-split")
		return sp != nil && sp.GetDirection() == "auto"
	})
	if sp := findByKeySuffix(tree, "main-split"); sp == nil || sp.GetDirection() != "auto" {
		t.Fatalf("standalone main-split = %+v; want direction=auto", sp)
	}
}

func TestMakoGitCommitBranchStashDiscardHistoryAndKorean(t *testing.T) {
	repo, git := initTestRepo(t)
	scope := protocol.FolderScope(protocol.LocalScopeNode, repo)
	r := startBuiltinRig(t)

	w := r.watch(scope, "en", plugin.View{Standalone: false})
	tree := w.until("initial git state", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKindAndText(n, "tree_item", "notes.txt") != nil &&
			findByKeySuffix(n, "history-dag") != nil
	})

	// 1. Uncheck notes.txt and commit the other 3 files via commit-btn + form values.
	notesItem := findByKindAndText(tree, "tree_item", "notes.txt")
	r.sendEvent(scope, notesItem.GetId(), "check", "off", nil)
	tree = w.until("commit button reflects 3 files", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		btn := findByKeySuffix(n, "commit-btn")
		return btn != nil && btn.GetText() == "Commit 3 files"
	})

	commitMsg := findByKeySuffix(tree, "commit-msg")
	commitBtn := findByKeySuffix(tree, "commit-btn")
	r.sendEvent(scope, commitBtn.GetId(), protocol.UiTap, "", map[string]string{
		commitMsg.GetId(): "Fill in main",
	})

	tree = w.until("commit completed and reloaded", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		dag := findByKeySuffix(n, "history-dag")
		return dag != nil && len(dag.GetRows()) == 5 && dag.GetRows()[0].GetText() == "Fill in main"
	})
	if st := strings.TrimSpace(git("status", "--porcelain")); st != "?? notes.txt" {
		t.Fatalf("git status after partial commit = %q; want %q", st, "?? notes.txt")
	}
	if subj := strings.TrimSpace(git("log", "-1", "--format=%s")); subj != "Fill in main" {
		t.Fatalf("latest commit subject = %q; want %q", subj, "Fill in main")
	}

	// 2. Verify dag rows (HEAD strong dot, merge commit with 2 parents) and commit detail + diff intent.
	dag := findByKeySuffix(tree, "history-dag")
	if !dag.GetRows()[0].GetStrong() {
		t.Fatalf("HEAD dag row should have strong=true: %+v", dag.GetRows()[0])
	}
	mergeRow := dag.GetRows()[1]
	if mergeRow.GetText() != "Merge feature" || len(mergeRow.GetParents()) != 2 {
		t.Fatalf("expected merge commit row with 2 parents, got %+v", mergeRow)
	}
	r.sendEvent(scope, dag.GetId(), protocol.UiChange, mergeRow.GetId(), nil)
	tree = w.until("commit detail loaded", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKeySuffix(n, "commit-detail") != nil && findByKindAndText(n, "button", "feature.go") != nil
	})
	commitFileBtn := findByKindAndText(tree, "button", "feature.go")
	diffDone := r.sendEvent(scope, commitFileBtn.GetId(), protocol.UiTap, "", nil)
	if len(diffDone.GetOpen()) != 1 || diffDone.GetOpen()[0].GetCommit() != mergeRow.GetId() || diffDone.GetOpen()[0].GetPath() != "feature.go" {
		t.Fatalf("unexpected commit file diff intent: %+v", diffDone.GetOpen())
	}

	// 3. Create branch "next" via dialog, then switch back to "main".
	newBranchItem := findByKeySuffix(tree, "br-new")
	if newBranchItem == nil {
		t.Fatal("missing New branch… menu item")
	}
	r.sendEvent(scope, newBranchItem.GetId(), protocol.UiTap, "", nil)
	tree = w.until("branch dialog open", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		dlg := findByKeySuffix(n, "git-dialog")
		return dlg != nil && dlg.GetText() == "New branch"
	})
	dlgInput := findByKeySuffix(tree, "dialog-input")
	dlgSubmit := findByKeySuffix(tree, "dlg-submit")
	r.sendEvent(scope, dlgSubmit.GetId(), protocol.UiTap, "", map[string]string{
		dlgInput.GetId(): "next",
	})
	tree = w.until("switched to next branch", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		bm := findByKeySuffix(n, "branch-menu")
		return bm != nil && bm.GetText() == "next"
	})
	if cur := strings.TrimSpace(git("rev-parse", "--abbrev-ref", "HEAD")); cur != "next" {
		t.Fatalf("HEAD branch = %q; want next", cur)
	}

	mainBranchItem := findByKindAndText(tree, "menu_item", "main")
	if mainBranchItem == nil {
		t.Fatal("missing main branch menu_item")
	}
	r.sendEvent(scope, mainBranchItem.GetId(), protocol.UiTap, "", nil)
	tree = w.until("switched back to main", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		bm := findByKeySuffix(n, "branch-menu")
		return bm != nil && bm.GetText() == "main"
	})

	// 4. Stash notes.txt via dialog, expand Stashes section, then Pop it back.
	stashMenuItem := findByKeySuffix(tree, "op-stash")
	r.sendEvent(scope, stashMenuItem.GetId(), protocol.UiTap, "", nil)
	tree = w.until("stash dialog open", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		dlg := findByKeySuffix(n, "git-dialog")
		return dlg != nil && dlg.GetText() == "Stash changes"
	})
	dlgInput = findByKeySuffix(tree, "dialog-input")
	dlgSubmit = findByKeySuffix(tree, "dlg-submit")
	r.sendEvent(scope, dlgSubmit.GetId(), protocol.UiTap, "", map[string]string{
		dlgInput.GetId(): "notes for later",
	})
	tree = w.until("notes.txt stashed", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKindAndText(n, "text", "No changes.") != nil
	})
	if st := strings.TrimSpace(git("status", "--porcelain")); st != "" {
		t.Fatalf("expected clean tree after stash, got %q", st)
	}

	// Expand Stashes and tap Pop.
	stashesToggle := findByKeySuffix(tree, "sec-stashes-toggle")
	r.sendEvent(scope, stashesToggle.GetId(), protocol.UiTap, "", nil)
	tree = w.until("stashes expanded", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKindAndText(n, "button", "Pop") != nil
	})
	popBtn := findByKindAndText(tree, "button", "Pop")
	r.sendEvent(scope, popBtn.GetId(), protocol.UiTap, "", nil)
	tree = w.until("stash popped", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKindAndText(n, "tree_item", "notes.txt") != nil
	})

	// 5. Discard all changes -> danger confirm dialog -> confirm -> notes.txt deleted.
	discardAllItem := findByKeySuffix(tree, "op-discard-all")
	r.sendEvent(scope, discardAllItem.GetId(), protocol.UiTap, "", nil)
	tree = w.until("discard confirm dialog open", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		dlg := findByKeySuffix(n, "git-dialog")
		return dlg != nil && dlg.GetVariant() == "danger"
	})
	if _, err := os.Stat(filepath.Join(repo, "notes.txt")); err != nil {
		t.Fatalf("notes.txt should still exist before confirming discard: %v", err)
	}
	confirmBtn := findByKeySuffix(tree, "dlg-confirm")
	r.sendEvent(scope, confirmBtn.GetId(), protocol.UiTap, "", nil)
	w.until("notes.txt discarded", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKeySuffix(n, "git-dialog") == nil && findByKindAndText(n, "text", "No changes.") != nil
	})
	if _, err := os.Stat(filepath.Join(repo, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("notes.txt still exists after confirmed discard: %v", err)
	}

	// 6. Korean localization parity check.
	w.cancel()
	wKo := r.watch(scope, "ko", plugin.View{Standalone: false})
	koTree := wKo.until("korean labels rendered", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKindAndText(n, "button", "변경 사항") != nil &&
			findByKindAndText(n, "button", "히스토리") != nil &&
			findByKindAndText(n, "button", "브랜치") != nil &&
			findByKindAndText(n, "button", "스태시") != nil
	})
	if findByKindAndText(koTree, "text", "변경 사항이 없습니다.") == nil {
		t.Fatal("missing Korean empty changes text")
	}
}

type fakeForgeThrottleHost struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (f *fakeForgeThrottleHost) BranchStatus(_ context.Context, _ plugin.GitTarget, _ string) (*plugin.ForgeBranch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail {
		return nil, os.ErrDeadlineExceeded
	}
	return &plugin.ForgeBranch{
		Forge:    "github",
		HasPR:    true,
		PRNumber: 77,
		PRTitle:  "Polish git plugin",
		PRState:  "OPEN",
		PRChecks: "success",
	}, nil
}

func (f *fakeForgeThrottleHost) PullRequestDetail(_ context.Context, _ plugin.GitTarget, number int32) (*plugin.ForgePRDetail, error) {
	return &plugin.ForgePRDetail{
		Forge:   "github",
		Number:  number,
		Title:   "Polish git plugin",
		State:   "OPEN",
		HeadOID: "abc1234",
		Checks:  "success",
	}, nil
}

func (f *fakeForgeThrottleHost) FailedLogs(_ context.Context, _ plugin.GitTarget, _ int32, _ int32) (*plugin.ForgeFailedLogs, error) {
	return &plugin.ForgeFailedLogs{}, nil
}

type fakeGitScenarioHost struct {
	mu       sync.Mutex
	status   *protocol.GitChanges
	history  *protocol.GitHistory
	watchCbs []func()
	runFn    func(ctx context.Context, t plugin.GitTarget, r *protocol.GitRun, worktree bool) (string, error)
}

func (g *fakeGitScenarioHost) Allowed(plugin.GitTarget) bool { return true }
func (g *fakeGitScenarioHost) Status(_ context.Context, _ plugin.GitTarget, base string) (*protocol.GitChanges, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := &protocol.GitChanges{
		Root:      g.status.Root,
		Branch:    g.status.Branch,
		Upstream:  g.status.Upstream,
		Ahead:     g.status.Ahead,
		Behind:    g.status.Behind,
		Files:     g.status.Files,
		Branches:  g.status.Branches,
		Operation: g.status.Operation,
		Worktree:  g.status.Worktree,
	}
	_ = base
	return out, nil
}
func (g *fakeGitScenarioHost) Log(_ context.Context, _ plugin.GitTarget, _ int, _ string) (*protocol.GitHistory, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.history, nil
}
func (g *fakeGitScenarioHost) Show(_ context.Context, _ plugin.GitTarget, hash string) (*protocol.GitCommitInfo, error) {
	return &protocol.GitCommitInfo{Commit: &protocol.GitCommit{Hash: hash, Subject: "Init"}}, nil
}
func (g *fakeGitScenarioHost) Run(ctx context.Context, t plugin.GitTarget, r *protocol.GitRun, worktree bool) (string, error) {
	if g.runFn != nil {
		return g.runFn(ctx, t, r, worktree)
	}
	return "", nil
}
func (g *fakeGitScenarioHost) Watch(ctx context.Context, _ plugin.GitTarget, changed func()) error {
	g.mu.Lock()
	g.watchCbs = append(g.watchCbs, changed)
	g.mu.Unlock()
	<-ctx.Done()
	return nil
}
func (g *fakeGitScenarioHost) fireWatch() {
	g.mu.Lock()
	cbs := append([]func(){}, g.watchCbs...)
	g.mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
}

func TestMakoGitForgeThrottleConflictCopyAndBaseContextMenu(t *testing.T) {
	fg := &fakeGitScenarioHost{
		status: &protocol.GitChanges{
			Root:     "/tmp/fake-repo",
			Branch:   "feat/ui",
			Branches: []string{"main", "feat/ui"},
			Files: []*protocol.GitFile{
				{Path: "a.go", Status: "conflicted", Added: 2, Removed: 1},
			},
			Operation: "merge",
			Worktree:  &protocol.Worktree{Repo: "/tmp/fake-repo", Path: "/tmp/fake-repo", Branch: "feat/ui", Base: "main"},
		},
		history: &protocol.GitHistory{
			Current:     "abc1234",
			Fingerprint: "fp-1",
			Commits: []*protocol.GitCommit{
				{Hash: "abc1234", Author: "Ada", Subject: "Initial"},
			},
		},
	}
	ff := &fakeForgeThrottleHost{}

	base := t.TempDir()
	dir := filepath.Join(base, "plugins")
	data := filepath.Join(base, "plugin-data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	m := plugin.New(plugin.Config{
		Dir:     dir,
		DataDir: data,
		Poll:    50 * time.Millisecond,
		Log:     slog.New(slog.NewTextHandler(io.MultiWriter(logs), &slog.HandlerOptions{Level: slog.LevelDebug})),
		Limits:  plugin.Limits{CPU: 10 * time.Second, Wall: 20 * time.Second, Memory: 32 << 20, Updates: 20 * time.Millisecond},
		Bundled: builtin.FS,
		Git:     fg,
		Forge:   ff,
	})
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	go func() { defer close(doneCh); m.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-doneCh
		if t.Failed() {
			t.Logf("plugin manager log:\n%s", logs.String())
		}
	})
	r := &testRig{t: t, m: m, cancel: cancel, done: doneCh, logs: logs}
	r.waitPlugin("mako-git", func(p *protocol.PluginInfo) bool {
		return p.GetBundled() && (p.GetState() == protocol.PluginReady || p.GetState() == protocol.PluginRunning)
	})

	scope := protocol.FolderScope(protocol.LocalScopeNode, "/tmp/fake-repo")
	w := r.watch(scope, "en", plugin.View{Standalone: false})
	tree := w.until("initial forge & 1 conflict banner", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKeySuffix(n, "forge-pr") != nil && findByKeySuffix(n, "op-msg") != nil
	})

	// 1. Singular conflict banner + separate non-truncated hint text.
	if msg := findByKeySuffix(tree, "op-msg"); msg == nil || msg.GetText() != "A merge stopped on 1 conflict." {
		t.Fatalf("op-msg = %v; want singular conflict text", msg)
	}
	if hint := findByKeySuffix(tree, "op-hint"); hint == nil || hint.GetText() != "Resolve them, then continue, or abort." {
		t.Fatalf("op-hint = %v; want separate wrap-friendly hint", hint)
	}
	// Placeholder propagated on icon-only buttons and tree_item checkbox.
	if pullBtn := findByKeySuffix(tree, "tb-pull"); pullBtn == nil || pullBtn.GetPlaceholder() != "Pull" {
		t.Fatalf("tb-pull placeholder = %q; want Pull", pullBtn.GetPlaceholder())
	}
	if fileItem := findByKindAndText(tree, "tree_item", "a.go"); fileItem == nil || fileItem.GetPlaceholder() != "Commit a.go" {
		t.Fatalf("a.go placeholder = %q; want Commit a.go", fileItem.GetPlaceholder())
	}

	ff.mu.Lock()
	callsAfterInit := ff.calls
	ff.mu.Unlock()
	if callsAfterInit != 1 {
		t.Fatalf("initial forge calls = %d; want 1", callsAfterInit)
	}

	// 2. File-only status change (same branch/HEAD/ahead/behind) does NOT re-call forge.
	fg.mu.Lock()
	fg.status.Files = append(fg.status.Files, &protocol.GitFile{Path: "b.go", Status: "conflicted", Added: 1})
	fg.mu.Unlock()
	fg.fireWatch()
	tree = w.until("2 conflicts rendered", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		msg := findByKeySuffix(n, "op-msg")
		return msg != nil && msg.GetText() == "A merge stopped on 2 conflicts."
	})
	ff.mu.Lock()
	callsAfterFileReload := ff.calls
	ff.mu.Unlock()
	if callsAfterFileReload != 1 {
		t.Fatalf("forge calls after file-only reload = %d; want 1 (throttled)", callsAfterFileReload)
	}

	// 3. Explicit refresh forces forge fetch; transient error preserves previous s.forge on same branch.
	ff.mu.Lock()
	ff.fail = true
	ff.mu.Unlock()
	refreshItem := findByKeySuffix(tree, "op-refresh")
	if refreshItem == nil {
		t.Fatal("missing op-refresh menu item")
	}
	r.sendEvent(scope, refreshItem.GetId(), protocol.UiTap, "", nil)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ff.mu.Lock()
		c := ff.calls
		ff.mu.Unlock()
		if c >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	ff.mu.Lock()
	callsAfterRefresh := ff.calls
	ff.mu.Unlock()
	if callsAfterRefresh < 2 {
		t.Fatalf("expected explicit refresh to re-fetch forge, calls = %d", callsAfterRefresh)
	}
	// Previous forge PR button must still be preserved after transient error.
	tree = w.until("forge PR preserved on transient error", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKeySuffix(n, "forge-pr") != nil
	})
	if findByKeySuffix(tree, "forge-pr") == nil {
		t.Fatal("forge PR was cleared on transient error on same branch")
	}

	// 4. Switching to "since main" keeps read-only file context menu without Discard / Add to .gitignore.
	baseToggle := findByKeySuffix(tree, "base-toggle")
	if baseToggle == nil {
		t.Fatal("missing base-toggle button")
	}
	r.sendEvent(scope, baseToggle.GetId(), protocol.UiTap, "", nil)
	tree = w.until("since main mode active", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		btn := findByKeySuffix(n, "base-toggle")
		return btn != nil && btn.GetText() == "since main"
	})
	fileItem := findByKindAndText(tree, "tree_item", "a.go")
	if fileItem == nil || fileItem.GetContextMenu() != "change-file" {
		t.Fatalf("expected read-only change-file contextMenu in since-base mode, got %+v", fileItem)
	}
	fileMenu := findByKeySuffix(tree, "change-file")
	if fileMenu == nil {
		t.Fatal("missing change-file context menu in since-base mode")
	}
	if findByKindAndText(fileMenu, "menu_item", "Open diff") == nil ||
		findByKindAndText(fileMenu, "menu_item", "Open file") == nil ||
		findByKindAndText(fileMenu, "menu_item", "Copy path") == nil {
		t.Fatalf("missing read-only items in change-file menu: %+v", fileMenu.GetChildren())
	}
	if findByKindAndText(fileMenu, "menu_item", "Discard changes…") != nil ||
		findByKindAndText(fileMenu, "menu_item", "Add to .gitignore") != nil {
		t.Fatalf("mutating items must be omitted in since-base mode: %+v", fileMenu.GetChildren())
	}
}

func TestMakoGitHTTPSAuthDialogAndRetry(t *testing.T) {
	var (
		mu       sync.Mutex
		authed   bool
		authReq  gitstat.HTTPSAuthRequest
		fetchCnt int
	)
	fg := &fakeGitScenarioHost{
		status: &protocol.GitChanges{
			Root:     "/tmp/https-repo",
			Branch:   "main",
			Upstream: "origin/main",
			Branches: []string{"main"},
		},
		history: &protocol.GitHistory{
			Current:     "abc1234",
			Fingerprint: "fp-https",
			Commits: []*protocol.GitCommit{
				{Hash: "abc1234", Author: "Ada", Subject: "Initial"},
			},
		},
		runFn: func(_ context.Context, _ plugin.GitTarget, r *protocol.GitRun, _ bool) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			switch r.GetOp() {
			case protocol.GitOpAuth:
				if err := json.Unmarshal([]byte(r.GetMessage()), &authReq); err != nil {
					return "", err
				}
				authed = true
				return "", nil
			case protocol.GitOpFetch:
				fetchCnt++
				if !authed {
					return "", errors.New("fatal: could not read Username for 'https://git.example.com:3000': terminal prompts disabled")
				}
				return "Fetched origin", nil
			default:
				return "", nil
			}
		},
	}

	base := t.TempDir()
	dir := filepath.Join(base, "plugins")
	data := filepath.Join(base, "plugin-data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	m := plugin.New(plugin.Config{
		Dir:     dir,
		DataDir: data,
		Poll:    50 * time.Millisecond,
		Log:     slog.New(slog.NewTextHandler(io.MultiWriter(logs), &slog.HandlerOptions{Level: slog.LevelDebug})),
		Limits:  plugin.Limits{CPU: 10 * time.Second, Wall: 20 * time.Second, Memory: 32 << 20, Updates: 20 * time.Millisecond},
		Bundled: builtin.FS,
		Git:     fg,
	})
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	go func() { defer close(doneCh); m.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-doneCh
		if t.Failed() {
			t.Logf("plugin manager log:\n%s", logs.String())
		}
	})
	r := &testRig{t: t, m: m, cancel: cancel, done: doneCh, logs: logs}
	r.waitPlugin("mako-git", func(p *protocol.PluginInfo) bool {
		return p.GetBundled() && (p.GetState() == protocol.PluginReady || p.GetState() == protocol.PluginRunning)
	})

	scope := protocol.FolderScope(protocol.LocalScopeNode, "/tmp/https-repo")
	w := r.watch(scope, "en", plugin.View{Standalone: false})
	tree := w.until("initial toolbar rendered", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKeySuffix(n, "op-fetch") != nil
	})

	// 1. Trigger Fetch -> fails with HTTPS auth error -> auth dialog opens with masked password input.
	fetchItem := findByKeySuffix(tree, "op-fetch")
	r.sendEvent(scope, fetchItem.GetId(), protocol.UiTap, "", nil)
	tree = w.until("HTTPS auth dialog opens", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKeySuffix(n, "dialog-auth-pass") != nil && findByKeySuffix(n, "note-auth") != nil
	})
	passNode := findByKeySuffix(tree, "dialog-auth-pass")
	if passNode.GetVariant() != "password" {
		t.Fatalf("dialog-auth-pass variant = %q, want password", passNode.GetVariant())
	}

	// 2. Submit credentials -> plugin sends sanitized GitOpAuth and immediately retries fetch.
	submitBtn := findByKeySuffix(tree, "dlg-submit")
	if submitBtn == nil {
		t.Fatal("missing dlg-submit button in auth dialog")
	}
	userNode := findByKeySuffix(tree, "dialog-auth-user")
	saveNode := findByKeySuffix(tree, "dialog-auth-save")
	r.sendEvent(scope, submitBtn.GetId(), protocol.UiTap, "", map[string]string{
		userNode.GetId(): "alice",
		passNode.GetId(): "pat-secret-99",
		saveNode.GetId(): "on",
	})

	w.until("dialog closes and retry fetch succeeds", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		note := findByKeySuffix(n, "note-text")
		return findByKeySuffix(n, "git-dialog") == nil && note != nil && note.GetText() == "Fetched origin"
	})

	mu.Lock()
	defer mu.Unlock()
	if !authed {
		t.Fatal("expected GitOpAuth to be called")
	}
	if authReq.URL != "" {
		t.Fatalf("plugin GitOpAuth must strip URL, got %q", authReq.URL)
	}
	if authReq.Username != "alice" || authReq.Password != "pat-secret-99" || !authReq.Save {
		t.Fatalf("unexpected authReq: %+v", authReq)
	}
	if fetchCnt != 2 {
		t.Fatalf("fetchCnt = %d, want 2 (initial fail + auto retry)", fetchCnt)
	}
}
