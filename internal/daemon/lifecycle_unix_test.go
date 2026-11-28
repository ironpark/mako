//go:build !windows

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/lifecycle"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/repotrust"
	"github.com/ironpark/mako/internal/session"
)

// shellHooks skips a test that needs git where there is none.
func shellHooks(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// emptyGitRepo is a new repository with branch checked out, no commits in it.
func emptyGitRepo(t *testing.T, branch string) string {
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

// serveHookNow serves a hook of session id as its agent ran it at seq,
// and returns once the daemon has taken it.
func serveHookNow(t *testing.T, d *Daemon, id string, seq int64, input string) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.serveHook(t.Context(), server)
	}()
	if err := json.NewEncoder(client).Encode(hookMessage{Session: id, Seq: seq, Input: json.RawMessage(input)}); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	<-done
}

// linesOf waits for file to hold n lines of session id, and returns them,
// the session's ID cut from each.
func linesOf(t *testing.T, file, id string, n int) []string {
	t.Helper()
	var got []string
	waitForWithin(t, 10*time.Second, "hooks did not run", func() bool {
		got = recorded(file, id)
		return len(got) >= n
	})
	return got
}

// recorded are the lines of file of session id, its ID cut from each.
func recorded(file, id string) []string {
	data, _ := os.ReadFile(file)
	var out []string
	for line := range strings.Lines(string(data)) {
		if rest, ok := strings.CutSuffix(strings.TrimSpace(line), " "+id); ok {
			out = append(out, rest)
		}
	}
	return out
}

// A session's hooks run on its start, once on each turn its agent ends and
// once on its agent's session's end, each told the branch checked out in
// the session's folder, which the session's own record leaves empty. A
// hook repeated tells nothing again; a session woken runs no start's
// hooks, nor one closed an end's; one whose program exits on its own runs
// its end's; a shell's runs none.
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
	hook := func(id string, seq int64, input string) { t.Helper(); serveHookNow(t, d, id, seq, input) }
	now := func() int64 { return time.Now().UnixNano() }

	out := filepath.Join(t.TempDir(), "events")
	record := `printf '%s %s %s\n' "$AGENT_MAKO_EVENT" "$AGENT_MAKO_BRANCH" "$AGENT_MAKO_SESSION" >> ` + sh(out)
	writeHooks(t, d.dir,
		lifecycle.Hook{Event: lifecycle.SessionStart, Command: record},
		lifecycle.Hook{Event: lifecycle.TurnDone, Command: record},
		lifecycle.Hook{Event: lifecycle.SessionEnd, Command: record},
		lifecycle.Hook{Event: lifecycle.TurnDone, Command: "echo not codex >> " + sh(out), Agents: []string{"codex"}},
	)
	repo := emptyGitRepo(t, "feat/hooks")
	s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: repo})
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	check := func(what string, want ...string) {
		t.Helper()
		if got := linesOf(t, out, id, len(want)); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: %q, want %q", what, got, want)
		}
	}
	want := []string{"on_session_start feat/hooks"}
	check("after start", want...)

	before := now()
	hook(id, now(), `{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`)
	if s.Info().GetStatus() != protocol.AgentWorking {
		t.Fatal("prompt not reported")
	}
	hook(id, now(), `{"hook_event_name":"Stop"}`)
	hook(id, now(), `{"hook_event_name":"Stop"}`)  // the same end, again
	hook(id, before, `{"hook_event_name":"Stop"}`) // a stale end, of a turn before
	hook(id, now(), `{"hook_event_name":"SessionEnd"}`)
	hook(id, now(), `{"hook_event_name":"SessionEnd"}`)
	// Each event's hooks run after the last's: one turn's end, and one
	// session's, ran once.
	want = append(want, "on_turn_done feat/hooks", "on_session_end feat/hooks")
	check("after a turn and the end", want...)

	// Woken, as a daemon restarted wakes it, it is no new session.
	d.sessions.closeAll()
	if _, err := d.attachSession(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	hook(id, now(), `{"hook_event_name":"UserPromptSubmit","prompt":"again"}`)
	hook(id, now(), `{"hook_event_name":"Stop"}`)
	want = append(want, "on_turn_done feat/hooks")
	check("after waking", want...)
	// Closed, it ran none of an end's.
	if err := d.closeSession(id); err != nil {
		t.Fatal(err)
	}

	// A program that exits on its own, its agent telling no end, as agy
	// and opencode tell none, runs the end's hooks.
	quiet, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: repo})
	if err != nil {
		t.Fatal(err)
	}
	if got := linesOf(t, out, quiet.ID(), 1); got[0] != "on_session_start feat/hooks" {
		t.Fatalf("quiet start: %q", got)
	}
	if _, err := quiet.Write([]byte{4}); err != nil { // ^D: the fake agent exits at its input's end
		t.Fatal(err)
	}
	if got := linesOf(t, out, quiet.ID(), 2); got[1] != "on_session_end feat/hooks" {
		t.Fatalf("quiet end: %q", got)
	}

	// A shell's session runs none.
	shell, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: agentspec.ShellName, Project: repo})
	if err != nil {
		t.Fatal(err)
	}
	d.hookRuns.closeAndWait() // all that was queued ran
	if got := recorded(out, shell.ID()); len(got) != 0 {
		t.Fatalf("a shell's session ran %q", got)
	}
	if got := recorded(out, id); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("in the end: %q, want %q", got, want)
	}
	if got := recorded(out, quiet.ID()); len(got) != 2 {
		t.Fatalf("quiet in the end: %q", got)
	}
}

// Codex's app server tells of a turn's end itself, without Codex's own
// hooks; with them, its Stop hook tells of it too, before or after, its
// seq older or newer. on_turn_done runs once for each turn.
func TestCodexTurnDoneRunsOnceWithItsStopHookOrWithout(t *testing.T) {
	shellHooks(t)
	d := New(&config.Config{NodeID: "node"}, slog.New(slog.DiscardHandler))
	d.dir = t.TempDir()
	out := filepath.Join(t.TempDir(), "events")
	writeHooks(t, d.dir, lifecycle.Hook{Event: lifecycle.TurnDone, Command: `echo "$AGENT_MAKO_EVENT" >> ` + sh(out)})
	s, err := session.Start(session.Spec{ID: "codex-hooks", Agent: "codex", Project: t.TempDir(), Command: testSleepCommand(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d.sessions.add(s)
	b := &codexBridge{
		ctx: t.Context(), epoch: 1, tid: "thr-1", bound: true,
		itemIndices: map[string]int32{}, thinkingSeen: map[string]bool{}, fgItems: map[string]bool{},
		fgProcesses: map[string]bool{}, ignored: map[string]bool{}, abandoned: map[string]bool{},
	}
	base := time.Now()
	at := func(ms int) time.Time { return base.Add(time.Duration(ms) * 10 * time.Millisecond) }
	turn := func(method, id string, when time.Time) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"threadId": "thr-1", "turn": map[string]any{"id": id, "status": "completed"}})
		if err != nil {
			t.Fatal(err)
		}
		d.handleCodexNotification(s, b, codex.Notification{Method: method, Params: raw, EmittedAtMs: when.UnixMilli()}, 1)
	}
	stop := func(when time.Time) {
		t.Helper()
		serveHookNow(t, d, s.ID(), when.UnixNano(), `{"hook_event_name":"Stop"}`)
	}

	// Without Codex's hooks: the app server alone.
	turn("turn/started", "t1", at(0))
	turn("turn/completed", "t1", at(1))
	// The app server first; the Stop hook after, older and newer.
	turn("turn/started", "t2", at(2))
	turn("turn/completed", "t2", at(4))
	stop(at(3))
	stop(at(5))
	// The Stop hook first; the app server after.
	turn("turn/started", "t3", at(6))
	stop(at(7))
	turn("turn/completed", "t3", at(8))

	d.hookRuns.closeAndWait()
	data, _ := os.ReadFile(out)
	if got := strings.Count(string(data), lifecycle.TurnDone); got != 3 {
		t.Fatalf("on_turn_done ran %d times for 3 turns:\n%s", got, data)
	}
}

// A rewind's new session goes on with the conversation rewound: it is no
// new session's start.
func TestARewindStartsNoSession(t *testing.T) {
	if !startsSession(t.Context(), false) || startsSession(t.Context(), true) || startsSession(rewinding(t.Context()), false) {
		t.Fatal("startsSession")
	}
}

// A hooks.json that does not read cleanly runs none of its hooks, its
// good ones neither.
func TestAWrongHooksFileRunsNone(t *testing.T) {
	shellHooks(t)
	d := New(nil, slog.New(slog.DiscardHandler))
	d.dir = t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	writeHooks(t, d.dir,
		lifecycle.Hook{Event: lifecycle.TurnDone, Command: "touch " + sh(marker)},
		lifecycle.Hook{Event: lifecycle.TurnDone, Command: "touch " + sh(marker), TimeoutSec: -1},
	)
	d.runLifecycle(t.Context(), lifecycle.Event{Name: lifecycle.TurnDone, SessionID: "s1", Agent: "claude", Project: t.TempDir()})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a hook of a file that does not read cleanly ran")
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

	repo := emptyGitRepo(t, "main")
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
