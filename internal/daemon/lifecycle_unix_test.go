//go:build !windows

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/lifecycle"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/repotrust"
)

// shellHooks skips a test that needs git where there is none.
func shellHooks(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// gitRepo is a new repository with branch checked out, no commits in it.
func gitRepo(t *testing.T, branch string) string {
	t.Helper()
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "-b", branch, repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return repo
}

// writeHooks writes the user's hooks to the configuration directory dir.
func writeHooks(t *testing.T, dir string, hooks ...lifecycle.Hook) {
	t.Helper()
	data, err := json.Marshal(hooks)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lifecycle.GlobalFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// sh quotes s for sh.
func sh(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// lines waits for file to hold n lines, and returns them.
func lines(t *testing.T, file string, n int) []string {
	t.Helper()
	var got []string
	waitForWithin(t, 10*time.Second, "hooks did not run", func() bool {
		data, _ := os.ReadFile(file)
		got = strings.Split(strings.TrimSpace(string(data)), "\n")
		return len(data) > 0 && len(got) >= n
	})
	return got
}

// A session's hooks run on its start, on each turn its agent ends and on
// its agent's session's end, each told the branch checked out in the
// session's folder, which the session's own record leaves empty.
func TestLifecycleHooksRunOnTheSessionsEventsWithItsBranch(t *testing.T) {
	shellHooks(t)
	command, root := fakeAgent(t, "claude")
	cfg := &config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{"claude": {Command: []string{command}, Env: map[string]string{"MAKO_FAKE_AGENT": "claude", "MAKO_FAKE_ROOT": root, "CLAUDE_CONFIG_DIR": root}}}}
	d := New(cfg, nil)
	d.mcp = nil
	d.dir = t.TempDir()
	if err := d.loadSessions(d.dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)
	run, err := os.MkdirTemp("", "mako-hooks")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	d.hookBinary = "/bin/makod"
	d.listenHooks(t.Context(), run)

	out := filepath.Join(t.TempDir(), "events")
	record := `printf '%s %s %s\n' "$AGENT_MAKO_EVENT" "$AGENT_MAKO_BRANCH" "$AGENT_MAKO_SESSION" >> ` + sh(out)
	writeHooks(t, d.dir,
		lifecycle.Hook{Event: lifecycle.SessionStart, Command: record},
		lifecycle.Hook{Event: lifecycle.TurnDone, Command: record},
		lifecycle.Hook{Event: lifecycle.SessionEnd, Command: record},
		lifecycle.Hook{Event: lifecycle.TurnDone, Command: "echo not codex >> " + sh(out), Agents: []string{"codex"}},
	)
	repo := gitRepo(t, "feat/hooks")
	s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: repo})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"on_session_start feat/hooks " + s.ID()}
	if got := lines(t, out, 1); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("after start: %q", got)
	}

	t.Setenv("MAKO_SESSION", s.ID())
	socket := filepath.Join(run, "hooks.sock")
	runHook([]string{socket}, strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`), io.Discard)
	waitFor(t, "prompt not reported", func() bool { return s.Info().GetStatus() == protocol.AgentWorking })
	runHook([]string{socket}, strings.NewReader(`{"hook_event_name":"Stop"}`), io.Discard)
	want = append(want, "on_turn_done feat/hooks "+s.ID())
	if got := lines(t, out, 2); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("after a turn: %q", got)
	}
	runHook([]string{socket}, strings.NewReader(`{"hook_event_name":"SessionEnd"}`), io.Discard)
	want = append(want, "on_session_end feat/hooks "+s.ID())
	if got := lines(t, out, 3); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("after the end: %q", got)
	}
}

// syncBuffer is a log written from the hooks' goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
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

// A repository's .mako/hooks.toml runs nothing until the user trusts it as
// it reads, by the request the CLI and the app send, and nothing again
// once it changes; the daemon's log says what to run to trust it.
func TestRepositoryHooksRunOnlyWhileTrusted(t *testing.T) {
	shellHooks(t)
	var log syncBuffer
	d := New(nil, slog.New(slog.NewTextHandler(&log, nil)))
	d.dir = t.TempDir()
	d.repoTrust = repotrust.Open(d.dir)

	repo := gitRepo(t, "main")
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(filepath.Join(repo, ".mako"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	file := filepath.Join(repo, ".mako", "hooks.toml")
	content := "[[hook]]\nevent = \"on_turn_done\"\ncommand = '''echo \"$AGENT_MAKO_PROJECT\" >> \"" + marker + "\"'''\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// The user's own hooks run, trusted or not, before the repository's.
	global := filepath.Join(t.TempDir(), "global")
	writeHooks(t, d.dir, lifecycle.Hook{Event: lifecycle.TurnDone, Command: "echo x >> " + sh(global)})

	ev := lifecycle.Event{Name: lifecycle.TurnDone, SessionID: "s1", Agent: "claude", Project: sub}
	ranIn := func() string {
		data, _ := os.ReadFile(marker)
		return strings.TrimSpace(string(data))
	}
	d.runLifecycle(t.Context(), ev)
	if got := ranIn(); got != "" {
		t.Fatalf("an untrusted repository's hook ran in %q", got)
	}
	if data, _ := os.ReadFile(global); string(data) != "x\n" {
		t.Fatalf("the user's own hook did not run: %q", data)
	}
	// It names the file in the repository's main work tree, links resolved.
	real, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "mako repo-trust allow "+real) {
		t.Fatalf("the log does not say how to trust it:\n%s", log.String())
	}

	// Trusted as the CLI and the app trust it: a project in a folder of
	// the repository runs the hooks of its root, in that folder.
	if _, err := d.repoTrustLocal(&protocol.RepoTrust{Op: protocol.RepoTrustAllow, Path: file, Sha256: repotrust.Hash([]byte(content))}); err != nil {
		t.Fatal(err)
	}
	d.runLifecycle(t.Context(), ev)
	if got := ranIn(); got != sub {
		t.Fatalf("a trusted hook ran in %q, want %q", got, sub)
	}

	// A session in another worktree of the repository runs the main work
	// tree's file, trusted there, in its own folder: one trust for the
	// repository, not one for each worktree.
	wt := filepath.Join(t.TempDir(), "wt")
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("commit", "-q", "--allow-empty", "-m", "init")
	git("worktree", "add", "-q", "-b", "feat/wt", wt)
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	d.runLifecycle(t.Context(), lifecycle.Event{Name: lifecycle.TurnDone, SessionID: "s2", Agent: "claude", Project: wt})
	if got := ranIn(); got != wt {
		t.Fatalf("a worktree's session ran its repository's hook in %q, want %q", got, wt)
	}

	// Changed, by a commit pulled or an agent, it runs nothing again.
	if err := os.WriteFile(file, []byte(content+"[[hook]]\nevent = \"on_turn_done\"\ncommand = '''touch \""+marker+".2\"'''\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	d.runLifecycle(t.Context(), ev)
	if got := ranIn(); got != "" {
		t.Fatalf("a changed file's hook ran in %q", got)
	}
	if _, err := os.Stat(marker + ".2"); err == nil {
		t.Fatal("a hook added after the trust ran")
	}
}

// A hook that hangs holds neither the session nor its agent's turns, and
// the daemon's shutdown ends it, with what it started: its hooks run under
// the daemon's context. Run reads the user's hooks from the configuration
// directory it resolves.
func TestLifecycleHooksEndWithTheDaemon(t *testing.T) {
	shellHooks(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MAKO_HOME", home)
	dir, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	marks := t.TempDir()
	started, pidFile := filepath.Join(marks, "started"), filepath.Join(marks, "pid")
	writeHooks(t, dir,
		lifecycle.Hook{Event: lifecycle.SessionStart, Command: "echo \"$AGENT_MAKO_SESSION\" > " + sh(started)},
		lifecycle.Hook{Event: lifecycle.SessionStart, TimeoutSec: 300, Command: "sleep 60 & echo $! > " + sh(pidFile) + "; wait"},
	)
	command, root := fakeAgent(t, "claude")
	cfg := config.Default()
	cfg.P2P.Disabled = true
	cfg.Discovery = config.Discovery{}
	cfg.Agents = map[string]agentspec.Agent{"claude": {Command: []string{command}, Env: map[string]string{"MAKO_FAKE_AGENT": "claude", "MAKO_FAKE_ROOT": root, "CLAUDE_CONFIG_DIR": root}}}
	d := New(cfg, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case err := <-runErr:
		t.Fatalf("Run exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not become ready")
	}

	s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	waitForWithin(t, 10*time.Second, "the user's hooks.json was not read", func() bool {
		data, _ := os.ReadFile(started)
		return strings.TrimSpace(string(data)) == s.ID()
	})
	var pid int
	waitForWithin(t, 10*time.Second, "the hanging hook did not start", func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil || !strings.HasSuffix(string(data), "\n") {
			return false
		}
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return pid > 0
	})
	if state := s.Info().GetState(); state != protocol.StateRunning {
		t.Fatalf("the session is %s while its hook hangs", state)
	}
	// Its agent's turns are told of meanwhile.
	if !s.ReportStatus(protocol.AgentDone, time.Now().UnixNano()) {
		t.Fatal("a turn's end was not taken while a hook hangs")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return while a hook hangs")
	}
	waitForWithin(t, 5*time.Second, "a hook's process outlived the daemon", func() bool {
		return syscall.Kill(pid, 0) != nil
	})
	// And none starts after it.
	d.sessionStartHooks(s)
	if d.hookRuns.start("late", func() {}) {
		t.Fatal("a hook was taken after shutdown")
	}
}
