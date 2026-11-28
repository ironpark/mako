package plugin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
)

type fakeGitHost struct {
	mu           sync.Mutex
	allowed      map[GitTarget]bool
	statusFn     func(ctx context.Context, t GitTarget, base string) (*protocol.GitChanges, error)
	logFn        func(ctx context.Context, t GitTarget, max int, known string) (*protocol.GitHistory, error)
	showFn       func(ctx context.Context, t GitTarget, hash string) (*protocol.GitCommitInfo, error)
	runFn        func(ctx context.Context, t GitTarget, r *protocol.GitRun, worktree bool) (string, error)
	watches      map[GitTarget][]func()
	activeWatch  atomic.Int32
	watchStarted chan GitTarget
	watchEnded   chan GitTarget
}

func newFakeGitHost() *fakeGitHost {
	return &fakeGitHost{
		allowed:      map[GitTarget]bool{},
		watches:      map[GitTarget][]func(){},
		watchStarted: make(chan GitTarget, 32),
		watchEnded:   make(chan GitTarget, 32),
	}
}

func (f *fakeGitHost) Allowed(t GitTarget) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.allowed[t]
}

func (f *fakeGitHost) Status(ctx context.Context, t GitTarget, base string) (*protocol.GitChanges, error) {
	if f.statusFn != nil {
		return f.statusFn(ctx, t, base)
	}
	return &protocol.GitChanges{
		Root:     t.Path,
		Branch:   "main",
		Upstream: "origin/main",
		Ahead:    2,
		Behind:   1,
		Files: []*protocol.GitFile{
			{Path: "a.go", Status: "modified", Added: 5, Removed: 2},
		},
		Branches:       []string{"main", "feat"},
		RemoteBranches: []string{"origin/main"},
		Stashes: []*protocol.GitStash{
			{Index: 0, Message: "wip", Time: timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))},
		},
		Worktree: &protocol.Worktree{Repo: t.Path, Path: t.Path, Branch: "main", Base: "main"},
	}, nil
}

func (f *fakeGitHost) Log(ctx context.Context, t GitTarget, max int, known string) (*protocol.GitHistory, error) {
	if f.logFn != nil {
		return f.logFn(ctx, t, max, known)
	}
	return &protocol.GitHistory{
		Current:     "abc1234",
		Fingerprint: "fp-1",
		Commits: []*protocol.GitCommit{
			{
				Hash:    "abc1234",
				Parents: []string{"def5678"},
				Author:  "Alice",
				Time:    timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
				Subject: "Initial commit",
				Refs:    []string{"HEAD -> main"},
			},
		},
	}, nil
}

func (f *fakeGitHost) Show(ctx context.Context, t GitTarget, hash string) (*protocol.GitCommitInfo, error) {
	if f.showFn != nil {
		return f.showFn(ctx, t, hash)
	}
	return &protocol.GitCommitInfo{
		Commit: &protocol.GitCommit{Hash: hash, Author: "Alice", Subject: "Initial commit"},
		Email:  "alice@example.com",
		Body:   "Commit body",
		Files:  []*protocol.GitFile{{Path: "a.go", Status: "added", Added: 10}},
	}, nil
}

func (f *fakeGitHost) Run(ctx context.Context, t GitTarget, r *protocol.GitRun, worktree bool) (string, error) {
	if f.runFn != nil {
		return f.runFn(ctx, t, r, worktree)
	}
	return "ran " + r.GetOp(), nil
}

func (f *fakeGitHost) Watch(ctx context.Context, t GitTarget, changed func()) error {
	f.mu.Lock()
	f.watches[t] = append(f.watches[t], changed)
	f.mu.Unlock()
	f.activeWatch.Add(1)
	select {
	case f.watchStarted <- t:
	default:
	}
	<-ctx.Done()
	f.activeWatch.Add(-1)
	select {
	case f.watchEnded <- t:
	default:
	}
	return nil
}

func (f *fakeGitHost) fire(t GitTarget) {
	f.mu.Lock()
	cbs := append([]func(){}, f.watches[t]...)
	f.mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
}

type fakeForgeHost struct {
	fn       func(ctx context.Context, t GitTarget, branch string) (*ForgeBranch, error)
	detailFn func(ctx context.Context, t GitTarget, number int32) (*ForgePRDetail, error)
	logsFn   func(ctx context.Context, t GitTarget, runID int64, maxLines int32) (*ForgeFailedLogs, error)
}

func (f *fakeForgeHost) BranchStatus(ctx context.Context, t GitTarget, branch string) (*ForgeBranch, error) {
	if f.fn != nil {
		return f.fn(ctx, t, branch)
	}
	return &ForgeBranch{
		Forge:     "github",
		HasPR:     true,
		PRNumber:  42,
		PRTitle:   "Add git plugin",
		PRState:   "open",
		PRChecks:  "pass",
		Runs:      3,
		RunsState: "pass",
		RunID:     101,
		RunHead:   "abc1234",
	}, nil
}

func (f *fakeForgeHost) PullRequestDetail(ctx context.Context, t GitTarget, number int32) (*ForgePRDetail, error) {
	if f.detailFn != nil {
		return f.detailFn(ctx, t, number)
	}
	return &ForgePRDetail{
		Forge:   "github",
		Number:  number,
		Title:   "Add git plugin",
		State:   "OPEN",
		HeadOID: "abc1234",
	}, nil
}

func (f *fakeForgeHost) FailedLogs(ctx context.Context, t GitTarget, runID int64, maxLines int32) (*ForgeFailedLogs, error) {
	if f.logsFn != nil {
		return f.logsFn(ctx, t, runID, maxLines)
	}
	return &ForgeFailedLogs{Logs: "test failed"}, nil
}

func TestGitReadForgeAndAsyncRun(t *testing.T) {
	fg := newFakeGitHost()
	var ranWorktree atomic.Bool
	var ranOp atomic.Value
	fg.runFn = func(_ context.Context, _ GitTarget, r *protocol.GitRun, wt bool) (string, error) {
		ranOp.Store(r.GetOp())
		ranWorktree.Store(wt)
		if r.GetOp() == protocol.GitOpPush {
			return "", errors.New("rejected by remote")
		}
		return "committed " + r.GetMessage(), nil
	}
	ff := &fakeForgeHost{}

	r := startRig(t, Config{Limits: testLimits, Git: fg, Forge: ff})
	r.write("gitplug", map[string]string{
		"plugin.toml": manifest("gitplug", []string{"git:read", "git:write", "forge:read"}, `
[[contributes.block]]
id = "panel"
title = "Git Panel"
scope = "folder"
`),
		"main.js": `export default {
  activate(mako) {
    this._mako = mako;
    mako.blocks.register("panel", {
      state: { summary: "", done: null, errs: {} },
      async load(state, ctx) {
        const st = await ctx.git.status();
        const hist = await ctx.git.history({ max: 10 });
        const show = await ctx.git.showCommit("abc1234");
        const forge = await ctx.forge.branchStatus(st.branch);
        state.summary = st.branch + ":" + st.files.length + ":" + hist.commits.length + ":" + show.email + ":pr" + forge.prNumber;
        // Verify git.run is rejected outside user gesture (during load).
        try {
          await ctx.git.run({ op: "commit", message: "no-gesture" });
          state.errs.noGesture = "unexpected-ok";
        } catch (e) {
          state.errs.noGesture = String(e.message || e);
        }
        // Verify unauthorized path is rejected.
        try {
          await mako.git.status({ target: { path: "/etc/secret" } });
          state.errs.unauth = "unexpected-ok";
        } catch (e) {
          state.errs.unauth = String(e.message || e);
        }
      },
      render(state, ui) {
        return ui.column([
          ui.text("summary=" + state.summary),
          ui.text("noGesture=" + (state.errs.noGesture || "")),
          ui.text("unauth=" + (state.errs.unauth || "")),
          ui.text("done=" + (state.done ? (state.done.op + ":" + state.done.ok + ":" + state.done.output + ":" + state.done.error) : "none")),
          ui.button("Commit").onTap("commit"),
          ui.button("Push").onTap("push"),
          ui.button("BadOp").onTap("badop"),
        ]);
      },
      on: {
        async commit(state, ) {
          const ctx = mako.blocks.context("panel");
          await ctx.git.run({ op: "commit", message: "hello", worktree: true });
        },
        async push(state) {
          const ctx = mako.blocks.context("panel");
          await ctx.git.run({ op: "push" });
        },
        async badop(state) {
          const ctx = mako.blocks.context("panel");
          await ctx.git.run({ op: "discard_hunk" });
        },
      },
    });
  },
  on: {
    gitDone(ev) {
      const st = this._mako.blocks.state("panel", ev.scopeKey);
      st.done = ev;
    },
  },
};`,
	})
	r.state("gitplug", protocol.PluginWaiting)
	r.grant("gitplug")

	repoPath := "/tmp/test-git-repo"
	scopeKey := protocol.FolderScope("", repoPath)
	w := r.watchScopedView("plugin:gitplug/panel", scopeKey, "en", View{})
	tree := w.until("loaded summary", func(tr *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(tr, "summary=main:1:1:alice@example.com:pr42")
	})
	if !hasText(tree, "gesture") {
		t.Fatalf("expected gesture rejection during load; tree=%v", texts(tree))
	}
	if !hasText(tree, "not allowed") {
		t.Fatalf("expected unauthorized path rejection; tree=%v", texts(tree))
	}

	// discard_hunk must be rejected even with a user gesture.
	badBtn := find(tree, "BadOp")
	if badBtn == nil {
		t.Fatal("missing BadOp button")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := r.m.Event(ctx, &protocol.BlockEvent{Block: "plugin:gitplug/panel", Scope: scopeKey, Node: badBtn.GetId(), Kind: "tap"}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("discard_hunk error = %v; want not allowed", err)
	}

	// Valid gesture git.run executes async and delivers on.gitDone + re-renders.
	commitBtn := find(tree, "Commit")
	if _, err := r.m.Event(ctx, &protocol.BlockEvent{Block: "plugin:gitplug/panel", Scope: scopeKey, Node: commitBtn.GetId(), Kind: "tap"}); err != nil {
		t.Fatalf("commit tap failed: %v", err)
	}
	w.until("gitDone commit", func(tr *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(tr, "done=commit:true:committed hello:")
	})
	if ranOp.Load() != protocol.GitOpCommit || !ranWorktree.Load() {
		t.Fatalf("ranOp=%v worktree=%v", ranOp.Load(), ranWorktree.Load())
	}

	// Failing git.run delivers ok=false with error text.
	pushBtn := find(tree, "Push")
	if _, err := r.m.Event(ctx, &protocol.BlockEvent{Block: "plugin:gitplug/panel", Scope: scopeKey, Node: pushBtn.GetId(), Kind: "tap"}); err != nil {
		t.Fatalf("push tap failed: %v", err)
	}
	w.until("gitDone push error", func(tr *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(tr, "done=push:false::rejected by remote")
	})
}

func TestGitWatchDebounceDirtyReloadAndCleanup(t *testing.T) {
	oldDebounce := gitWatchDebounce
	gitWatchDebounce = 25 * time.Millisecond
	t.Cleanup(func() { gitWatchDebounce = oldDebounce })

	fg := newFakeGitHost()
	var loadCount atomic.Int32
	blockLoad := make(chan struct{})
	inLoad2 := make(chan struct{})
	fg.statusFn = func(_ context.Context, t GitTarget, _ string) (*protocol.GitChanges, error) {
		n := loadCount.Add(1)
		if n == 2 {
			close(inLoad2)
			<-blockLoad
		}
		return &protocol.GitChanges{Root: t.Path, Branch: fmt.Sprintf("rev-%d", n)}, nil
	}

	r := startRig(t, Config{Limits: testLimits, Git: fg})
	r.write("watcher", map[string]string{
		"plugin.toml": manifest("watcher", []string{"git:read"}, `
[[contributes.block]]
id = "panel"
title = "Watcher"
scope = "folder"
`),
		"main.js": `export default {
  activate(mako) {
    mako.blocks.register("panel", {
      state: { branch: "" },
      async load(state, ctx) {
        await ctx.git.watch(); // idempotent on every load
        const st = await ctx.git.status();
        state.branch = st.branch;
      },
      render(state, ui) {
        return ui.text("branch=" + state.branch);
      },
    });
  },
};`,
	})
	r.state("watcher", protocol.PluginWaiting)
	r.grant("watcher")

	repoPath := "/tmp/watch-repo"
	scopeKey := protocol.FolderScope("", repoPath)
	w := r.watchScopedView("plugin:watcher/panel", scopeKey, "en", View{})
	w.until("rev-1", func(tr *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(tr, "branch=rev-1")
	})

	select {
	case <-fg.watchStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("git watch never started")
	}
	if fg.activeWatch.Load() != 1 {
		t.Fatalf("activeWatch = %d; want 1", fg.activeWatch.Load())
	}

	// Rapid burst of events debounces into one reload (rev-2).
	target := GitTarget{Node: "", Path: repoPath}
	fg.fire(target)
	fg.fire(target)
	fg.fire(target)

	// Wait until load #2 is in-flight, then fire another event to exercise the dirty bit.
	select {
	case <-inLoad2:
	case <-time.After(5 * time.Second):
		t.Fatal("second load never started")
	}
	fg.fire(target)
	time.Sleep(50 * time.Millisecond) // let debounce fire while load #2 is still running
	close(blockLoad)

	// Dirty bit must trigger load #3 after load #2 completes.
	w.until("rev-3", func(tr *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(tr, "branch=rev-3")
	})
	// Still only 1 active watch (idempotent across reloads).
	if fg.activeWatch.Load() != 1 {
		t.Fatalf("activeWatch after reloads = %d; want 1", fg.activeWatch.Load())
	}

	// Unsubscribing the last viewer cleans up the watch automatically.
	w.cancel()
	select {
	case <-fg.watchEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("git watch was not cleaned up on unsubscribe")
	}
	if fg.activeWatch.Load() != 0 {
		t.Fatalf("activeWatch after unsubscribe = %d; want 0", fg.activeWatch.Load())
	}
}

func TestWindowIntentsGestureAndValidation(t *testing.T) {
	fg := newFakeGitHost()
	r := startRig(t, Config{Limits: testLimits, Git: fg})
	r.write("intents", map[string]string{
		"plugin.toml": manifest("intents", nil, `
[[contributes.block]]
id = "panel"
title = "Intents"
scope = "folder"
`),
		"main.js": `export default {
  activate(mako) {
    mako.blocks.register("panel", {
      state: { loadErr: "" },
      async load(state, ctx) {
        try {
          await ctx.ui.copy("nope");
          state.loadErr = "unexpected-ok";
        } catch (e) {
          state.loadErr = String(e.message || e);
        }
      },
      render(state, ui) {
        return ui.column([
          ui.text("loadErr=" + state.loadErr),
          ui.button("AllIntents").onTap("all"),
          ui.button("BadPath").onTap("badpath"),
          ui.button("HugeCopy").onTap("hugecopy"),
        ]);
      },
      on: {
        async all(state) {
          const ctx = mako.blocks.context("panel");
          await ctx.ui.copy("deadbeef");
          await ctx.ui.openDiff({ repo: ctx.folder, path: "a.go", status: "modified", added: 3, removed: 1, preview: true });
          await ctx.ui.openFile({ path: ctx.folder + "/a.go", line: 12 });
          await ctx.ui.openForge({ kind: "pr", number: 7 });
          await ctx.ui.createPullRequest();
        },
        async badpath(state) {
          const ctx = mako.blocks.context("panel");
          await ctx.ui.openFile({ path: "/etc/passwd" });
        },
        async hugecopy(state) {
          const ctx = mako.blocks.context("panel");
          await ctx.ui.copy("x".repeat(70000));
        },
      },
    });
  },
};`,
	})
	r.state("intents", protocol.PluginReady)

	repoPath := "/tmp/intents-repo"
	scopeKey := protocol.FolderScope("", repoPath)
	w := r.watchScopedView("plugin:intents/panel", scopeKey, "en", View{})
	tree := w.until("loaded", func(tr *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(tr, "loadErr=") && hasText(tr, "only in answer")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// All 5 intents populate PluginDone.
	allBtn := find(tree, "AllIntents")
	done, err := r.m.Event(ctx, &protocol.BlockEvent{Block: "plugin:intents/panel", Scope: scopeKey, Node: allBtn.GetId(), Kind: "tap"})
	if err != nil {
		t.Fatalf("AllIntents failed: %v", err)
	}
	if done.GetCopyText() != "deadbeef" {
		t.Fatalf("CopyText = %q; want deadbeef", done.GetCopyText())
	}
	if len(done.GetOpen()) != 4 {
		t.Fatalf("Open len = %d; want 4 (%+v)", len(done.GetOpen()), done.GetOpen())
	}
	if done.GetOpen()[0].GetKind() != "diff" || done.GetOpen()[0].GetPath() != "a.go" || !done.GetOpen()[0].GetPreview() {
		t.Fatalf("open[0] = %+v", done.GetOpen()[0])
	}
	if done.GetOpen()[1].GetKind() != "file" || done.GetOpen()[1].GetPath() != repoPath+"/a.go" || done.GetOpen()[1].GetLine() != 12 {
		t.Fatalf("open[1] = %+v", done.GetOpen()[1])
	}
	if done.GetOpen()[2].GetKind() != "pr" || done.GetOpen()[2].GetNumber() != 7 || done.GetOpen()[2].GetFolder() != repoPath {
		t.Fatalf("open[2] = %+v", done.GetOpen()[2])
	}
	if done.GetOpen()[3].GetKind() != "create_pr" || done.GetOpen()[3].GetFolder() != repoPath {
		t.Fatalf("open[3] = %+v", done.GetOpen()[3])
	}

	// Disallowed path rejected.
	badBtn := find(tree, "BadPath")
	if _, err := r.m.Event(ctx, &protocol.BlockEvent{Block: "plugin:intents/panel", Scope: scopeKey, Node: badBtn.GetId(), Kind: "tap"}); err == nil {
		t.Fatal("expected BadPath to fail")
	}

	// Oversized copyText rejected.
	hugeBtn := find(tree, "HugeCopy")
	if _, err := r.m.Event(ctx, &protocol.BlockEvent{Block: "plugin:intents/panel", Scope: scopeKey, Node: hugeBtn.GetId(), Kind: "tap"}); err == nil {
		t.Fatal("expected HugeCopy to fail")
	}
}

func TestOpenDiffRejectsArbitraryAncestorRepo(t *testing.T) {
	fg := newFakeGitHost()
	r := startRig(t, Config{Limits: testLimits, Git: fg})
	r.write("escape", map[string]string{
		"plugin.toml": manifest("escape", nil, `
[[contributes.block]]
id = "panel"
title = "Escape"
scope = "folder"
`),
		"main.js": `export default {
  activate(mako) {
    mako.blocks.register("panel", {
      state: {},
      render(_state, ui) {
        return ui.column([
          ui.button("RootAncestor").onTap("rootAncestor"),
          ui.button("ParentAncestor").onTap("parentAncestor"),
          ui.button("SameFolder").onTap("sameFolder"),
          ui.button("EmptyRepo").onTap("emptyRepo"),
          ui.button("AllowedOther").onTap("allowedOther"),
        ]);
      },
      on: {
        async rootAncestor() {
          const ctx = mako.blocks.context("panel");
          await ctx.ui.openDiff({ repo: "/", path: "etc/passwd" });
        },
        async parentAncestor() {
          const ctx = mako.blocks.context("panel");
          await ctx.ui.openDiff({ repo: "/tmp/sub", path: "secret.txt" });
        },
        async sameFolder() {
          const ctx = mako.blocks.context("panel");
          await ctx.ui.openDiff({ repo: ctx.folder, path: "a.go" });
        },
        async emptyRepo() {
          const ctx = mako.blocks.context("panel");
          await ctx.ui.openDiff({ path: "a.go" });
        },
        async allowedOther() {
          const ctx = mako.blocks.context("panel");
          await ctx.ui.openDiff({ repo: "/tmp/sub", path: "project/a.go" });
        },
      },
    });
  },
};`,
	})
	r.state("escape", protocol.PluginReady)

	repoPath := "/tmp/sub/project"
	scopeKey := protocol.FolderScope("", repoPath)
	w := r.watchScopedView("plugin:escape/panel", scopeKey, "en", View{})
	tree := w.until("rendered", func(tr *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return find(tr, "RootAncestor") != nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Ancestor directories "/" and "/tmp/sub" MUST be rejected when not allowed.
	for _, label := range []string{"RootAncestor", "ParentAncestor"} {
		btn := find(tree, label)
		if btn == nil {
			t.Fatalf("missing button %s", label)
		}
		if _, err := r.m.Event(ctx, &protocol.BlockEvent{
			Block: "plugin:escape/panel", Scope: scopeKey, Node: btn.GetId(), Kind: "tap",
		}); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("%s: err = %v; want not allowed", label, err)
		}
	}

	// Same folder and empty repo succeed.
	for _, label := range []string{"SameFolder", "EmptyRepo"} {
		btn := find(tree, label)
		done, err := r.m.Event(ctx, &protocol.BlockEvent{
			Block: "plugin:escape/panel", Scope: scopeKey, Node: btn.GetId(), Kind: "tap",
		})
		if err != nil {
			t.Fatalf("%s failed: %v", label, err)
		}
		if len(done.GetOpen()) != 1 || done.GetOpen()[0].GetKind() != "diff" {
			t.Fatalf("%s open = %+v; want 1 diff intent", label, done.GetOpen())
		}
	}

	// An explicitly allowed repo root (e.g. a registered repo ancestor) is accepted.
	fg.mu.Lock()
	fg.allowed[GitTarget{Node: "", Path: "/tmp/sub"}] = true
	fg.mu.Unlock()
	allowedBtn := find(tree, "AllowedOther")
	done, err := r.m.Event(ctx, &protocol.BlockEvent{
		Block: "plugin:escape/panel", Scope: scopeKey, Node: allowedBtn.GetId(), Kind: "tap",
	})
	if err != nil {
		t.Fatalf("AllowedOther failed after allowing /tmp/sub: %v", err)
	}
	if len(done.GetOpen()) != 1 || done.GetOpen()[0].GetRepo() != "/tmp/sub" {
		t.Fatalf("AllowedOther open = %+v; want repo=/tmp/sub", done.GetOpen())
	}
}

func TestGitUnavailableAndLimits(t *testing.T) {
	// Unavailable when Config.Git / Config.Forge are nil.
	m := New(Config{DataDir: t.TempDir()})
	p := newPlugin(m, "test-limits")
	h := &host{p: p}
	if _, err := h.GitStatus(context.Background(), nil); !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("GitStatus err = %v; want ErrGitUnavailable", err)
	}
	if _, err := h.ForgeBranchStatus(context.Background(), nil); !errors.Is(err, ErrForgeUnavailable) {
		t.Fatalf("ForgeBranchStatus err = %v; want ErrForgeUnavailable", err)
	}
	if _, err := h.ForgePullRequestDetail(context.Background(), nil); !errors.Is(err, ErrForgeUnavailable) {
		t.Fatalf("ForgePullRequestDetail err = %v; want ErrForgeUnavailable", err)
	}
	if _, err := h.ForgeFailedLogs(context.Background(), nil); !errors.Is(err, ErrForgeUnavailable) {
		t.Fatalf("ForgeFailedLogs err = %v; want ErrForgeUnavailable", err)
	}

	// Max 16 watches enforced; 17th fails; Unwatch frees a slot.
	fg := newFakeGitHost()
	m2 := New(Config{DataDir: t.TempDir(), Git: fg, Forge: &fakeForgeHost{}})
	defer m2.shutdown()
	p2 := newPlugin(m2, "test-watches")
	p2.activeInstance = "panel@folder:local/L3RtcA"
	h2 := &host{p: p2}
	for i := range maxGitWatches {
		targetPath := fmt.Sprintf("/tmp/repo-%d", i)
		fg.allowed[GitTarget{Path: targetPath}] = true
		resp, err := h2.GitWatch(context.Background(), &hostapi.GitServiceWatchRequest{
			Target: &hostapi.GitTarget{Path: targetPath},
		})
		if err != nil || resp.GetRoot() != targetPath {
			t.Fatalf("watch %d = (%v, %v)", i, resp, err)
		}
	}
	// Idempotent re-watch on existing slot succeeds even when full.
	if _, err := h2.GitWatch(context.Background(), &hostapi.GitServiceWatchRequest{
		Target: &hostapi.GitTarget{Path: "/tmp/repo-0"},
	}); err != nil {
		t.Fatalf("idempotent re-watch failed: %v", err)
	}
	// 17th distinct watch fails.
	fg.allowed[GitTarget{Path: "/tmp/repo-extra"}] = true
	if _, err := h2.GitWatch(context.Background(), &hostapi.GitServiceWatchRequest{
		Target: &hostapi.GitTarget{Path: "/tmp/repo-extra"},
	}); err == nil || !strings.Contains(err.Error(), "16") {
		t.Fatalf("17th watch err = %v; want max watches error", err)
	}
	// Unwatch frees a slot so repo-extra succeeds.
	if _, err := h2.GitUnwatch(context.Background(), &hostapi.GitServiceUnwatchRequest{
		Target: &hostapi.GitTarget{Path: "/tmp/repo-0"},
	}); err != nil {
		t.Fatalf("GitUnwatch failed: %v", err)
	}
	if _, err := h2.GitWatch(context.Background(), &hostapi.GitServiceWatchRequest{
		Target: &hostapi.GitTarget{Path: "/tmp/repo-extra"},
	}); err != nil {
		t.Fatalf("watch after unwatch failed: %v", err)
	}
	p2.stopAllGitWatches()

	// Rate limits: git reads (120/min), git runs (30/min), forge reads (30/min).
	p3 := newPlugin(m2, "test-rates")
	p3.gesture = true
	h3 := &host{p: p3}
	fg.allowed[GitTarget{Path: "/tmp/rate-repo"}] = true
	reqTarget := &hostapi.GitTarget{Path: "/tmp/rate-repo"}
	for i := range gitReadsPerMin {
		if _, err := h3.GitStatus(context.Background(), &hostapi.GitServiceStatusRequest{Target: reqTarget}); err != nil {
			t.Fatalf("read %d failed: %v", i, err)
		}
	}
	if _, err := h3.GitStatus(context.Background(), &hostapi.GitServiceStatusRequest{Target: reqTarget}); err == nil {
		t.Fatal("121st git read unexpectedly succeeded")
	}
	for i := range forgeReadsPerMin {
		if _, err := h3.ForgeBranchStatus(context.Background(), &hostapi.ForgeServiceBranchStatusRequest{Target: reqTarget, Branch: "main"}); err != nil {
			t.Fatalf("forge %d failed: %v", i, err)
		}
	}
	if _, err := h3.ForgeBranchStatus(context.Background(), &hostapi.ForgeServiceBranchStatusRequest{Target: reqTarget, Branch: "main"}); err == nil {
		t.Fatal("31st forge read unexpectedly succeeded")
	}
	for i := range gitRunsPerMin {
		if _, err := h3.GitRun(context.Background(), &hostapi.GitServiceRunRequest{Target: reqTarget, Op: protocol.GitOpFetch}); err != nil {
			t.Fatalf("run %d failed: %v", i, err)
		}
	}
	if _, err := h3.GitRun(context.Background(), &hostapi.GitServiceRunRequest{Target: reqTarget, Op: protocol.GitOpFetch}); err == nil {
		t.Fatal("31st git run unexpectedly succeeded")
	}
	p3.cancelPending("done")

	// PullRequestDetail & FailedLogs happy path and argument validation.
	p4 := newPlugin(m2, "test-forge-autofix")
	h4 := &host{p: p4}
	if _, err := h4.ForgePullRequestDetail(context.Background(), &hostapi.ForgeServicePullRequestDetailRequest{
		Target: reqTarget, Number: -1,
	}); err == nil {
		t.Fatal("negative PR number should fail")
	}
	for _, id := range []float64{0, -1, 1.5, math.NaN(), math.Inf(1), 1<<53 + 2} {
		if _, err := h4.ForgeFailedLogs(context.Background(), &hostapi.ForgeServiceFailedLogsRequest{
			Target: reqTarget, RunId: id,
		}); err == nil {
			t.Fatalf("run_id %v should fail", id)
		}
	}
	det, err := h4.ForgePullRequestDetail(context.Background(), &hostapi.ForgeServicePullRequestDetailRequest{
		Target: reqTarget, Number: 42,
	})
	if err != nil || det.GetNumber() != 42 || det.GetHeadOid() != "abc1234" {
		t.Fatalf("ForgePullRequestDetail = (%+v, %v)", det, err)
	}
	logs, err := h4.ForgeFailedLogs(context.Background(), &hostapi.ForgeServiceFailedLogsRequest{
		Target: reqTarget, RunId: 101, MaxLines: 100,
	})
	if err != nil || logs.GetLogs() != "test failed" {
		t.Fatalf("ForgeFailedLogs = (%+v, %v)", logs, err)
	}
}

// TestForgeRunIDsPast32Bits keeps a GitHub run ID, past 2^31, exact from
// BranchStatus to FailedLogs, and FailedLogs keeps the end of a long log,
// where the failure is.
func TestForgeRunIDsPast32Bits(t *testing.T) {
	const runID = int64(46100334498)
	var gotID int64
	failure := "--- FAIL: TestThing (0.01s)\n    thing_test.go:12: want 2, got 3"
	long := strings.Repeat("ok  noise line\n", (maxGitOutput/15)+100) + failure
	ff := &fakeForgeHost{
		fn: func(context.Context, GitTarget, string) (*ForgeBranch, error) {
			return &ForgeBranch{Forge: "github", Runs: 1, RunsState: "failure", RunID: runID, RunHead: "abc1234"}, nil
		},
		logsFn: func(_ context.Context, _ GitTarget, id int64, _ int32) (*ForgeFailedLogs, error) {
			gotID = id
			return &ForgeFailedLogs{Logs: long}, nil
		},
	}
	fg := newFakeGitHost()
	fg.allowed[GitTarget{Path: "/repo"}] = true
	m := New(Config{DataDir: t.TempDir(), Git: fg, Forge: ff})
	defer m.shutdown()
	p := newPlugin(m, "test-forge-runid")
	h := &host{p: p}
	target := &hostapi.GitTarget{Path: "/repo"}

	bs, err := h.ForgeBranchStatus(context.Background(), &hostapi.ForgeServiceBranchStatusRequest{Target: target, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if bs.GetRunId() != float64(runID) || int64(bs.GetRunId()) != runID {
		t.Fatalf("run_id = %v; want %d", bs.GetRunId(), runID)
	}
	if bs.GetRunNumber() != 0 {
		t.Fatalf("run_number = %d; want 0 for an ID past 32 bits", bs.GetRunNumber())
	}

	logs, err := h.ForgeFailedLogs(context.Background(), &hostapi.ForgeServiceFailedLogsRequest{Target: target, RunId: bs.GetRunId()})
	if err != nil {
		t.Fatal(err)
	}
	if gotID != runID {
		t.Fatalf("FailedLogs got run %d; want %d", gotID, runID)
	}
	if !logs.GetTruncated() || len(logs.GetLogs()) > maxGitOutput {
		t.Fatalf("logs: truncated %v, %d bytes; want truncated within %d", logs.GetTruncated(), len(logs.GetLogs()), maxGitOutput)
	}
	if !strings.HasSuffix(logs.GetLogs(), failure) {
		t.Fatalf("logs lost their end: %q", logs.GetLogs()[len(logs.GetLogs())-80:])
	}
	if !strings.HasPrefix(logs.GetLogs(), "ok  noise line\n") {
		t.Fatalf("logs start mid-line: %q", logs.GetLogs()[:40])
	}
}

func TestCutTail(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
		cut  bool
	}{
		{"short", 10, "short", false},
		{"one\ntwo\nthree", 9, "three", true},
		{"one\ntwo\nthree", 5, "three", true},
		// Whole runes only: "한" is 3 bytes.
		{"ab한글", 5, "글", true},
		// No line break to start at: whole runes from the end.
		{"abcdef", 3, "def", true},
	} {
		got, cut := cutTail(tc.in, tc.n)
		if got != tc.want || cut != tc.cut {
			t.Errorf("cutTail(%q, %d) = (%q, %v); want (%q, %v)", tc.in, tc.n, got, cut, tc.want, tc.cut)
		}
		if !utf8.ValidString(got) {
			t.Errorf("cutTail(%q, %d) = %q, not UTF-8", tc.in, tc.n, got)
		}
	}
}

// TestForgePullRequestDetailComments passes each comment's author's tie to
// the repository, bots, and the viewer through, and answers with the latest
// comments when there are more than it holds.
func TestForgePullRequestDetailComments(t *testing.T) {
	var comments []ForgeComment
	for i := range maxForgeComments + 20 {
		comments = append(comments, ForgeComment{Author: "alice", Body: fmt.Sprintf("comment %d", i), Association: "MEMBER"})
	}
	comments = append(comments, ForgeComment{Author: "renovate", Body: "bump", Bot: true, Association: "NONE"})
	ff := &fakeForgeHost{detailFn: func(_ context.Context, _ GitTarget, n int32) (*ForgePRDetail, error) {
		return &ForgePRDetail{Forge: "github", Number: n, State: "OPEN", Viewer: "me", Comments: comments}, nil
	}}
	fg := newFakeGitHost()
	fg.allowed[GitTarget{Path: "/repo"}] = true
	m := New(Config{DataDir: t.TempDir(), Git: fg, Forge: ff})
	defer m.shutdown()
	h := &host{p: newPlugin(m, "test-forge-comments")}
	det, err := h.ForgePullRequestDetail(context.Background(), &hostapi.ForgeServicePullRequestDetailRequest{
		Target: &hostapi.GitTarget{Path: "/repo"}, Number: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	cs := det.GetComments()
	if det.GetViewer() != "me" || len(cs) != maxForgeComments {
		t.Fatalf("viewer %q, %d comments; want me, %d", det.GetViewer(), len(cs), maxForgeComments)
	}
	if first := cs[0].GetBody(); first != "comment 21" {
		t.Fatalf("first comment %q; want the oldest of the latest, comment 21", first)
	}
	last := cs[len(cs)-1]
	if last.GetAuthor() != "renovate" || !last.GetBot() || last.GetAssociation() != "NONE" {
		t.Fatalf("last comment = %+v; want renovate's, a bot's", last)
	}
	if cs[0].GetAssociation() != "MEMBER" || cs[0].GetBot() {
		t.Fatalf("first comment = %+v; want a member's", cs[0])
	}

	// Long bodies stop at the byte budget, the latest kept.
	big := strings.Repeat("x", maxForgeComment)
	comments = nil
	for range 100 {
		comments = append(comments, ForgeComment{Author: "alice", Body: big})
	}
	det, err = h.ForgePullRequestDetail(context.Background(), &hostapi.ForgeServicePullRequestDetailRequest{
		Target: &hostapi.GitTarget{Path: "/repo"}, Number: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := maxForgeCommentBytes / maxForgeComment; len(det.GetComments()) != want {
		t.Fatalf("%d comments; want %d within the byte budget", len(det.GetComments()), want)
	}
}
