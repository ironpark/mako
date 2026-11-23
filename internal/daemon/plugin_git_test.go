package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
	"github.com/ironpark/mako/internal/workspace"
)

func initTestGitRepo(t *testing.T) (repo string, runGit func(dir string, args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo = t.TempDir()
	runGit = func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=Tester",
			"GIT_AUTHOR_EMAIL=tester@example.com",
			"GIT_COMMITTER_NAME=Tester",
			"GIT_COMMITTER_EMAIL=tester@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit(repo, "init", "-q", "-b", "main")
	runGit(repo, "config", "user.name", "Tester")
	runGit(repo, "config", "user.email", "tester@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(repo, "add", "README.md")
	runGit(repo, "commit", "-q", "-m", "initial commit")
	return repo, runGit
}

func TestDaemonGitHostAllowed(t *testing.T) {
	d := meshDaemon(t, "a")
	gh := d.pluginGitHost()

	dirProj := t.TempDir()
	dirSub := filepath.Join(dirProj, "nested")
	if err := os.MkdirAll(dirSub, 0o755); err != nil {
		t.Fatal(err)
	}
	dirSess := t.TempDir()
	dirSaved := t.TempDir()
	dirWT := t.TempDir()
	dirWTRepo := t.TempDir()
	dirOther := t.TempDir()
	remoteNode := "remote-node-1"

	delta := &protocol.WorkspaceState{
		Projects: map[string]*protocol.ProjectEntry{
			"p1": {
				CreatedAt: mark(),
				Name:      &protocol.StringReg{Value: "Proj"},
				Roots: map[string]*protocol.ProjectRootEntry{
					"r1": {
						CreatedAt: mark(),
						Locations: map[string]*protocol.ElementMeta{
							workspace.LocationKey(d.config().NodeID, dirProj): {AddedAt: mark()},
							workspace.LocationKey(remoteNode, "/srv/app"):     {AddedAt: mark()},
						},
					},
				},
			},
		},
	}
	if !d.workspace.edit(delta) {
		t.Fatal("workspace edit had no effect")
	}
	d.syncWorkspaceProjects()

	s, err := session.Start(session.Spec{
		ID: "live-sess", NodeID: d.config().NodeID, Agent: "claude", Project: dirSess,
		Command: []string{"sh", "-c", "sleep 30"}, Cols: 40, Rows: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	d.sessions.add(s)

	d.sessionMu.Lock()
	if d.saved == nil {
		d.saved = map[string]savedSession{}
	}
	d.saved["saved-wt"] = savedSession{
		Info: &protocol.SessionInfo{
			Id:      "saved-wt",
			NodeId:  d.config().NodeID,
			Project: dirSaved,
			Worktree: &protocol.Worktree{
				Repo:   dirWTRepo,
				Path:   dirWT,
				Branch: "feat/x",
				Base:   "main",
			},
		},
	}
	d.sessionMu.Unlock()

	allowedCases := []plugin.GitTarget{
		{Node: "", Path: dirProj},
		{Node: protocol.LocalScopeNode, Path: dirSub},
		{Node: d.config().NodeID, Path: dirSess},
		{Node: "", Path: dirSaved},
		{Node: "local", Path: dirWT},
		{Node: "", Path: dirWTRepo},
		{Node: remoteNode, Path: "/srv/app"},
		{Node: remoteNode, Path: "/srv/app/pkg"},
	}
	for _, tc := range allowedCases {
		if !gh.Allowed(tc) {
			t.Errorf("Allowed(%+v) = false, want true", tc)
		}
	}

	deniedCases := []plugin.GitTarget{
		{Node: "", Path: dirOther},
		{Node: "local", Path: "relative/path"},
		{Node: "", Path: filepath.Join(dirProj, "..", filepath.Base(dirOther))},
		{Node: "", Path: dirProj + "/../escape"},
		{Node: remoteNode, Path: "/srv/other"},
		{Node: remoteNode, Path: "/srv/app/../other"},
		{Node: "unknown-node", Path: dirProj},
	}
	for _, tc := range deniedCases {
		if gh.Allowed(tc) {
			t.Errorf("Allowed(%+v) = true, want false", tc)
		}
	}
}

func TestDaemonGitHostLocalStatusLogShowRunAndWorktree(t *testing.T) {
	d := meshDaemon(t, "a")
	c := dialDaemon(t, d)
	gh := d.pluginGitHost()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repo, runGit := initTestGitRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt-feat")
	runGit(repo, "worktree", "add", "-q", "-b", "feat/wt", wtPath, "main")

	wtMeta := &protocol.Worktree{
		Repo:   repo,
		Path:   wtPath,
		Branch: "feat/wt",
		Base:   "main",
		Start:  "main",
	}
	d.sessionMu.Lock()
	if d.saved == nil {
		d.saved = map[string]savedSession{}
	}
	d.saved["wt-sess"] = savedSession{
		Info: &protocol.SessionInfo{
			Id:       "wt-sess",
			NodeId:   d.config().NodeID,
			Project:  wtPath,
			Worktree: wtMeta,
		},
	}
	d.sessionMu.Unlock()

	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Main repo status has no session worktree.
	mainCh, err := gh.Status(ctx, plugin.GitTarget{Node: "local", Path: repo}, "")
	if err != nil {
		t.Fatal(err)
	}
	if mainCh.GetNotRepo() || mainCh.GetBranch() != "main" || mainCh.GetWorktree() != nil {
		t.Fatalf("main status = %+v", mainCh)
	}
	if len(mainCh.GetFiles()) != 1 || mainCh.GetFiles()[0].GetPath() != "notes.txt" {
		t.Fatalf("main files = %+v", mainCh.GetFiles())
	}

	// Worktree status populates GitChanges.Worktree (field 12) daemon-side.
	wtCh, err := gh.Status(ctx, plugin.GitTarget{Node: "", Path: wtPath}, "")
	if err != nil {
		t.Fatal(err)
	}
	if wtCh.GetWorktree() == nil || wtCh.GetWorktree().GetBranch() != "feat/wt" || wtCh.GetWorktree().GetBase() != "main" {
		t.Fatalf("worktree status missing Worktree: %+v", wtCh)
	}

	// Stream handler handleGitStatus shares the same code path and populates Worktree too.
	rpcCh, err := c.GitStatus(ctx, &protocol.GitStatus{Path: wtPath})
	if err != nil {
		t.Fatal(err)
	}
	if rpcCh.GetWorktree() == nil || rpcCh.GetWorktree().GetBranch() != "feat/wt" {
		t.Fatalf("handleGitStatus missing Worktree: %+v", rpcCh)
	}

	// Log + known fingerprint + Show.
	hist, err := gh.Log(ctx, plugin.GitTarget{Path: repo}, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist.GetCommits()) != 1 || hist.GetCommits()[0].GetSubject() != "initial commit" || hist.GetFingerprint() == "" {
		t.Fatalf("log = %+v", hist)
	}
	histAgain, err := gh.Log(ctx, plugin.GitTarget{Path: repo}, 20, hist.GetFingerprint())
	if err != nil {
		t.Fatal(err)
	}
	if !histAgain.GetUnchanged() {
		t.Fatalf("expected unchanged history, got %+v", histAgain)
	}
	info, err := gh.Show(ctx, plugin.GitTarget{Path: repo}, hist.GetCommits()[0].GetHash())
	if err != nil {
		t.Fatal(err)
	}
	if info.GetCommit().GetSubject() != "initial commit" || len(info.GetFiles()) != 1 {
		t.Fatalf("show = %+v", info)
	}

	// Run: commit notes.txt.
	if _, err := gh.Run(ctx, plugin.GitTarget{Path: repo}, &protocol.GitRun{
		Op:      protocol.GitOpCommit,
		Message: "add notes",
		Paths:   []string{"notes.txt"},
	}, false); err != nil {
		t.Fatalf("commit run: %v", err)
	}
	afterCommit, err := gh.Status(ctx, plugin.GitTarget{Path: repo}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(afterCommit.GetFiles()) != 0 {
		t.Fatalf("expected clean tree after commit, got %+v", afterCommit.GetFiles())
	}

	// Run MUST NOT trust a caller-supplied Worktree when worktree == false.
	if _, err := gh.Run(ctx, plugin.GitTarget{Path: wtPath}, &protocol.GitRun{
		Op:       protocol.GitOpRemoveWT,
		Worktree: wtMeta,
	}, false); err == nil {
		t.Fatal("Run accepted caller-supplied Worktree with worktree=false")
	}

	// Run with worktree == true on a non-worktree path must fail.
	if _, err := gh.Run(ctx, plugin.GitTarget{Path: repo}, &protocol.GitRun{
		Op: protocol.GitOpRemoveWT,
	}, true); err == nil {
		t.Fatal("Run with worktree=true succeeded on main repo without session worktree")
	}

	// Run with worktree == true on wtPath looks up wtMeta daemon-side and removes the worktree.
	if _, err := gh.Run(ctx, plugin.GitTarget{Path: wtPath}, &protocol.GitRun{
		Op: protocol.GitOpRemoveWT,
	}, true); err != nil {
		t.Fatalf("remove_worktree via daemon lookup failed: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still exists after remove_worktree: err=%v", err)
	}
}

func TestDaemonGitHostRemoteForwardingAndWatch(t *testing.T) {
	p := newBlockPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repo, runGit := initTestGitRepo(t)
	wtPath := filepath.Join(t.TempDir(), "remote-wt")
	runGit(repo, "worktree", "add", "-q", "-b", "feat/remote", wtPath, "main")

	p.server.sessionMu.Lock()
	if p.server.saved == nil {
		p.server.saved = map[string]savedSession{}
	}
	p.server.saved["srv-wt"] = savedSession{
		Info: &protocol.SessionInfo{
			Id:      "srv-wt",
			NodeId:  p.server.config().NodeID,
			Project: wtPath,
			Worktree: &protocol.Worktree{
				Repo:   repo,
				Path:   wtPath,
				Branch: "feat/remote",
				Base:   "main",
			},
		},
	}
	p.server.sessionMu.Unlock()

	remoteID := p.server.config().NodeID
	gh := p.phone.pluginGitHost()

	// Remote Status includes server-attached Worktree.
	ch, err := gh.Status(ctx, plugin.GitTarget{Node: remoteID, Path: wtPath}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ch.GetBranch() != "feat/remote" || ch.GetWorktree() == nil || ch.GetWorktree().GetBranch() != "feat/remote" {
		t.Fatalf("remote status = %+v", ch)
	}

	// Remote Log & Show.
	hist, err := gh.Log(ctx, plugin.GitTarget{Node: remoteID, Path: repo}, 10, "")
	if err != nil || len(hist.GetCommits()) != 1 {
		t.Fatalf("remote log = %+v, err = %v", hist, err)
	}
	info, err := gh.Show(ctx, plugin.GitTarget{Node: remoteID, Path: repo}, hist.GetCommits()[0].GetHash())
	if err != nil || info.GetCommit().GetSubject() != "initial commit" {
		t.Fatalf("remote show = %+v, err = %v", info, err)
	}

	// Remote Watch relays WatchFiles notifications from server to phone.
	watchCtx, watchCancel := context.WithCancel(ctx)
	changed := make(chan struct{}, 8)
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- gh.Watch(watchCtx, plugin.GitTarget{Node: remoteID, Path: repo}, func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		})
	}()
	time.Sleep(250 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(repo, "remote-edit.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(10 * time.Second):
		t.Fatal("remote Watch did not notify on worktree change")
	}
	watchCancel()
	if err := <-watchDone; err != nil {
		t.Fatalf("remote Watch returned error: %v", err)
	}
}

type failingTreeFileWatcher struct{}

func (f failingTreeFileWatcher) NewPathWatcher(logf func(string, ...any)) PathWatcher {
	return noopPathWatcher{}
}

func (f failingTreeFileWatcher) NewTreeWatcher(settle time.Duration) TreeWatcher {
	return failingTreeWatcher{}
}

type noopPathWatcher struct{}

func (noopPathWatcher) Set([]string) {}
func (noopPathWatcher) Run(ctx context.Context, _ chan<- []string) {
	<-ctx.Done()
}

type failingTreeWatcher struct{}

func (failingTreeWatcher) Set([]string) {}
func (failingTreeWatcher) Run(context.Context, chan<- []string) error {
	return errors.New("tree watch unsupported")
}

func TestDaemonGitHostWatchLocalFSFallbackAndSessionStamp(t *testing.T) {
	// 1. Live FS + index watch.
	t.Run("fs_and_index", func(t *testing.T) {
		d := meshDaemon(t, "watch-local")
		repo, runGit := initTestGitRepo(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		changed := make(chan struct{}, 8)
		wctx, wcancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- d.pluginGitHost().Watch(wctx, plugin.GitTarget{Node: "local", Path: repo}, func() {
				select {
				case changed <- struct{}{}:
				default:
				}
			})
		}()
		time.Sleep(250 * time.Millisecond)

		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		select {
		case <-changed:
		case <-time.After(10 * time.Second):
			t.Fatal("Watch did not fire on worktree file write")
		}

		// Drain any coalesced notifications, then stage & commit to exercise git ref/index watch.
		time.Sleep(350 * time.Millisecond)
		for len(changed) > 0 {
			<-changed
		}
		runGit(repo, "add", "a.txt")
		runGit(repo, "commit", "-q", "-m", "add a")
		select {
		case <-changed:
		case <-time.After(10 * time.Second):
			t.Fatal("Watch did not fire on git commit")
		}
		wcancel()
		if err := <-done; err != nil {
			t.Fatalf("Watch returned %v", err)
		}
	})

	// 2. Fallback stamp poll when TreeWatcher fails.
	t.Run("fallback_stamp_poll", func(t *testing.T) {
		prev := gitWatchFallbackInterval
		gitWatchFallbackInterval = 100 * time.Millisecond
		t.Cleanup(func() { gitWatchFallbackInterval = prev })

		d := meshDaemon(t, "watch-fallback")
		d.fileWatcher = failingTreeFileWatcher{}
		repo, runGit := initTestGitRepo(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		changed := make(chan struct{}, 8)
		wctx, wcancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- d.pluginGitHost().Watch(wctx, plugin.GitTarget{Path: repo}, func() {
				select {
				case changed <- struct{}{}:
				default:
				}
			})
		}()
		time.Sleep(150 * time.Millisecond)

		if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(repo, "add", "b.txt")
		runGit(repo, "commit", "-q", "-m", "fallback commit")

		select {
		case <-changed:
		case <-time.After(5 * time.Second):
			t.Fatal("fallback stamp poll did not fire after commit")
		}
		wcancel()
		if err := <-done; err != nil {
			t.Fatalf("Watch returned %v", err)
		}
	})

	// 3. Session GitStamp update notification (reproducing repoStamp).
	t.Run("session_git_stamp", func(t *testing.T) {
		prev := gitWatchFallbackInterval
		gitWatchFallbackInterval = time.Hour // isolate session stamp trigger
		t.Cleanup(func() { gitWatchFallbackInterval = prev })

		d := meshDaemon(t, "watch-stamp")
		d.fileWatcher = failingTreeFileWatcher{}
		repo, runGit := initTestGitRepo(t)
		wtPath := filepath.Join(t.TempDir(), "stamp-wt")
		runGit(repo, "worktree", "add", "-q", "-b", "feat/stamp", wtPath, "main")

		d.sessionMu.Lock()
		if d.saved == nil {
			d.saved = map[string]savedSession{}
		}
		d.saved["s-wt"] = savedSession{
			Info: &protocol.SessionInfo{
				Id:      "s-wt",
				NodeId:  d.config().NodeID,
				Project: wtPath,
			},
		}
		d.sessionMu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		changed := make(chan struct{}, 8)
		wctx, wcancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- d.pluginGitHost().Watch(wctx, plugin.GitTarget{Path: repo}, func() {
				select {
				case changed <- struct{}{}:
				default:
				}
			})
		}()
		time.Sleep(150 * time.Millisecond)

		// Commit inside the linked worktree, invalidate cached heads, and fire session change.
		if err := os.WriteFile(filepath.Join(wtPath, "wt.txt"), []byte("wt\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(wtPath, "add", "wt.txt")
		runGit(wtPath, "commit", "-q", "-m", "wt commit")
		d.heads.mu.Lock()
		d.heads.known = nil
		d.heads.mu.Unlock()
		d.changes.fire(false, true)

		select {
		case <-changed:
		case <-time.After(5 * time.Second):
			t.Fatal("Watch did not fire on linked worktree session GitStamp change")
		}
		wcancel()
		if err := <-done; err != nil {
			t.Fatalf("Watch returned %v", err)
		}
	})
}

func TestBundledMakoGitWatchOverDaemon(t *testing.T) {
	d := meshDaemon(t, "a")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	waitFor(t, "mako-git bundled plugin ready", func() bool {
		return d.plugins != nil && findPlugin(d.plugins.List(), "mako-git") != nil
	})

	repo, _ := initTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scope := protocol.FolderScope(protocol.LocalScopeNode, repo)

	stream, _, err := transport.Dial(ctx, d.Socket(), &protocol.Hello{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := stream.Send(protocol.TypeWatchBlock, "1", &protocol.WatchBlock{
		Block:      "plugin:mako-git/panel",
		Scope:      scope,
		Standalone: true,
	}); err != nil {
		t.Fatal(err)
	}

	var tree *protocol.UiNode
	deadline := time.After(15 * time.Second)
	for {
		type recvRes struct {
			env *protocol.Envelope
			err error
		}
		ch := make(chan recvRes, 1)
		go func() {
			env, err := stream.Recv()
			ch <- recvRes{env, err}
		}()
		select {
		case res := <-ch:
			if res.err != nil {
				t.Fatalf("recv: %v", res.err)
			}
			u, err := protocol.Body[*protocol.BlockUpdate](res.env)
			if err != nil {
				t.Fatalf("body: %v", err)
			}
			if u.GetError() != "" {
				t.Fatalf("mako-git block error: %s", u.GetError())
			}
			if u.GetTree() != nil {
				tree = u.GetTree()
			} else if len(u.GetOps()) > 0 && tree != nil {
				next, err := protocol.ApplyUi(tree, u.GetOps())
				if err != nil {
					t.Fatal(err)
				}
				tree = next
			}
			if tree != nil && strings.Contains(treeText(tree), "dirty.txt") {
				return
			}
		case <-deadline:
			t.Fatalf("mako-git never rendered dirty.txt; last tree=%q", treeText(tree))
		}
	}
}

func TestDaemonForgeHostHelpers(t *testing.T) {
	if got := forgeRunsState(nil); got != "" {
		t.Fatalf("forgeRunsState(nil) = %q", got)
	}
	if got := forgeRunsState([]*protocol.ForgeItem{{State: "completed", Checks: "success"}}); got != "success" {
		t.Fatalf("forgeRunsState(success) = %q", got)
	}
	if got := forgeRunsState([]*protocol.ForgeItem{
		{State: "completed", Checks: "success"},
		{State: "completed", Checks: "failure"},
	}); got != "failure" {
		t.Fatalf("forgeRunsState(failure) = %q", got)
	}
	if got := forgeRunsState([]*protocol.ForgeItem{
		{State: "completed", Checks: "failure"},
		{State: "in_progress", Checks: "pending"},
	}); got != "pending" {
		t.Fatalf("forgeRunsState(pending) = %q", got)
	}

	// Non-forge local repository returns a clean error rather than panicking.
	d := meshDaemon(t, "forge-local")
	repo, _ := initTestGitRepo(t)
	if _, err := d.pluginForgeHost().BranchStatus(context.Background(), plugin.GitTarget{Path: repo}, "main"); err == nil {
		t.Fatal("expected error for local repo without forge remote")
	}
}
