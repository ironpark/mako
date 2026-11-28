package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// refusedHooks is how many hook connections d has refused so far.
func refusedHooks(d *Daemon) int {
	r := &d.hookRefusals
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last.IsZero() {
		return 0
	}
	return 1 + r.unlogged
}

// forgeHook sends msg to the hook socket as a process that is not the
// session's would, and reads what comes back until the daemon hangs up.
func forgeHook(t *testing.T, socket string, msg hookMessage) []byte {
	t.Helper()
	conn, err := sendHook(socket, msg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	got, _ := io.ReadAll(conn)
	return got
}

// hideHookToken moves session's token file out of its hooks' reach, as a
// sandbox hides it from the other sessions' processes, until the returned
// func puts it back.
func hideHookToken(t *testing.T, socket, session string) func() {
	t.Helper()
	path := hookTokenFile(filepath.Dir(socket), session)
	if err := os.Rename(path, path+".hidden"); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Rename(path+".hidden", path); err != nil {
			t.Fatal(err)
		}
	}
}

// Any process of the user's reaches hooks.sock; only the session's own,
// carrying its token, speaks for it. Without the token, or with another
// session's, no event, status line, approval or copy is taken.
func TestHookSocketRefusesWhatIsNotTheSessionsOwn(t *testing.T) {
	d, a, socket := hookedDaemon(t)
	b, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	d.approvals.watch(true)
	seq := time.Now().UnixNano()
	next := func() int64 { seq++; return seq }
	stop := json.RawMessage(`{"hook_event_name":"Stop"}`)
	ask := json.RawMessage(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`)

	for _, c := range []struct {
		name string
		msg  hookMessage
	}{
		{"no token", hookMessage{Session: a.ID(), Seq: next(), Input: stop}},
		{"a made-up token", hookMessage{Session: a.ID(), Token: strings.Repeat("0", 64), Seq: next(), Input: stop}},
		{"another session's token", hookMessage{Session: a.ID(), Token: d.hookToken(b.ID()), Seq: next(), Input: stop}},
		{"the token for no session", hookMessage{Session: a.ID(), Token: d.hookToken(""), Seq: next(), Input: stop}},
		{"a token for an empty session", hookMessage{Token: d.hookToken(""), Seq: next(), Input: stop}},
		{"an approval with another session's token", hookMessage{Session: a.ID(), Token: d.hookToken(b.ID()), Seq: next(), Input: ask}},
		{"an approval with no token", hookMessage{Session: b.ID(), Seq: next(), Input: ask}},
		{"a status line with another session's token", hookMessage{Session: a.ID(), Token: d.hookToken(b.ID()), Kind: statusArg, Seq: next(),
			Input: json.RawMessage(`{"context_window":{"context_window_size":1000000}}`)}},
		{"a copy with no token", hookMessage{Session: a.ID(), Kind: clipArg, Seq: next(), Input: json.RawMessage(`"secret"`)}},
	} {
		before := refusedHooks(d)
		if got := forgeHook(t, socket, c.msg); len(got) != 0 {
			t.Fatalf("%s: answered %q", c.name, got)
		}
		if refusedHooks(d) != before+1 {
			t.Fatalf("%s: not refused", c.name)
		}
		for _, s := range []*session.Session{a, b} {
			if st := s.Info().GetStatus(); st == protocol.AgentDone || st == protocol.AgentWaiting {
				t.Fatalf("%s: session %s took it: status %s", c.name, s.ID(), st)
			}
			if s.Info().GetApproval() != nil {
				t.Fatalf("%s: session %s shows an approval", c.name, s.ID())
			}
			if s.Reported().GetWindow() != 0 {
				t.Fatalf("%s: session %s took a status line", c.name, s.ID())
			}
		}
	}

	// The hook as an agent runs it, in a process that cannot read the
	// session's token, is refused too.
	t.Setenv("MAKO_SESSION", a.ID())
	unhide := hideHookToken(t, socket, a.ID())
	before := refusedHooks(d)
	runHook([]string{socket}, strings.NewReader(string(stop)), io.Discard)
	waitFor(t, "a hook without its session's token not refused", func() bool { return refusedHooks(d) == before+1 })
	if a.Info().GetStatus() == protocol.AgentDone {
		t.Fatal("a hook without its session's token ended the turn")
	}
	unhide()

	// Each session's own token works for it, and only for it.
	if got := forgeHook(t, socket, hookMessage{Session: b.ID(), Token: d.hookToken(b.ID()), Seq: next(), Input: stop}); len(got) != 0 {
		t.Fatalf("answered %q", got)
	}
	waitFor(t, "b's own hook not taken", func() bool { return b.Info().GetStatus() == protocol.AgentDone })
	if a.Info().GetStatus() == protocol.AgentDone {
		t.Fatal("b's hook ended a's turn")
	}
	runHook([]string{socket}, strings.NewReader(string(stop)), io.Discard)
	waitFor(t, "a's own hook not taken", func() bool { return a.Info().GetStatus() == protocol.AgentDone })
}

// A refused connection is logged, but at a low rate: a process that keeps
// trying does not fill the log, and the next line says how many it left out.
func TestHookRefusalsAreLoggedAtALowRate(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	var r hookRefusals
	for range 5 {
		r.note(log, hookMessage{Session: "s"})
	}
	if n := strings.Count(buf.String(), "refused"); n != 1 {
		t.Fatalf("logged %d times: %s", n, buf.String())
	}
	if !strings.Contains(buf.String(), `why="no token"`) || !strings.Contains(buf.String(), "unlogged=0") {
		t.Fatalf("line %s", buf.String())
	}
	r.mu.Lock()
	r.last = time.Now().Add(-hookRefusalEvery)
	r.mu.Unlock()
	buf.Reset()
	r.note(log, hookMessage{Session: strings.Repeat("x", 1000), Kind: strings.Repeat("k", 1000), Token: "t"})
	if !strings.Contains(buf.String(), `why="wrong token"`) || !strings.Contains(buf.String(), "unlogged=4") {
		t.Fatalf("line %s", buf.String())
	}
	if len(buf.String()) > 400 {
		t.Fatalf("what the sender chose went to the log whole: %s", buf.String())
	}
}

// A token is the daemon's for as long as it runs: one started again makes
// new ones, and throws away the files of the old, so a process left from a
// session it ran before cannot report for the session restored under the
// same ID. A token's file is the user's alone.
func TestHookTokensAreNewEachTimeTheDaemonStarts(t *testing.T) {
	first, second := New(&config.Config{NodeID: "n"}, nil), New(&config.Config{NodeID: "n"}, nil)
	token := first.hookToken("s")
	if token == second.hookToken("s") {
		t.Fatal("two daemons made the same token")
	}
	if token == first.hookToken("t") {
		t.Fatal("two sessions share a token")
	}
	if token != first.hookToken("s") {
		t.Fatal("a session's token changed")
	}
	if token == first.liveToken("s") {
		t.Fatal("the hook token is the live token")
	}

	run, err := os.MkdirTemp("", "mako-tok")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	for _, d := range []*Daemon{first, second} {
		d.hookBinary = "/bin/makod"
	}
	first.listenHooks(t.Context(), run)
	if err := first.giveHookToken("../../s"); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(run, "hooks.sock")
	if got := readHookToken(socket, "../../s"); got != first.hookToken("../../s") {
		t.Fatalf("read %q", got)
	}
	path := hookTokenFile(run, "../../s")
	if filepath.Dir(path) != filepath.Join(run, hookTokenDir) {
		t.Fatalf("a session's ID took its token file to %s", path)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s: %v %v, want %v", p, fi.Mode().Perm(), err, want)
		}
	}
	second.listenHooks(t.Context(), run)
	if got := readHookToken(socket, "../../s"); got != "" {
		t.Fatalf("a daemon started again kept the old one's token %q", got)
	}
}

// TestHookProcess is the daemon's program run as an agent's hook, for the
// tests that run hooks in processes of their own: the test binary, as
// writeHookProgram runs it.
func TestHookProcess(t *testing.T) {
	if os.Getenv("MAKO_TEST_HOOK_PROCESS") == "" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if len(args) > 0 && args[0] == hookArg {
		runHook(args[1:], os.Stdin, os.Stdout)
	}
	os.Exit(0)
}

// writeHookProgram is a program that runs as the daemon's own does when an
// agent runs it as its hook.
func writeHookProgram(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "makod")
	body := "#!/bin/sh\nMAKO_TEST_HOOK_PROCESS=1 exec " + agentspec.JoinArgs([]string{exe, "-test.run=^TestHookProcess$", "--"}) + " \"$@\"\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeHookedAgent is an agent, called program, that says it takes hooks,
// and writes down the arguments and environment it was started with in
// outDir, by its session.
func writeHookedAgent(t *testing.T, program, outDir string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), program)
	script := `#!/bin/sh
case "$1" in
  --help|app-server|plugin) exit 0 ;;
  features) echo "hooks under_development true"; exit 0 ;;
esac
out=` + agentspec.JoinArgs([]string{outDir}) + `/"${MAKO_SESSION}"
for a in "$@"; do printf '%s\n' "$a"; done > "${out}.args"
env > "${out}.env.tmp"
printf '__ENV_DONE__\n' >> "${out}.env.tmp"
mv "${out}.env.tmp" "${out}.env"
exec sleep 60
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// filesUnder is what the regular files under dir hold, together.
func filesUnder(dir string) string {
	var all strings.Builder
	_ = filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err == nil && e.Type().IsRegular() {
			if b, err := os.ReadFile(path); err == nil {
				all.Write(b)
				all.WriteByte('\n')
			}
		}
		return nil
	})
	return all.String()
}

// runHookCommand runs hook, a shell command, with env and input, as an
// agent runs its hook.
func runHookCommand(t *testing.T, hook string, env map[string]string, input string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", hook)
	cmd.Env = []string{}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = strings.NewReader(input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook: %v: %s", err, out)
	}
}

// Every agent that hooks mako runs the daemon's hook command, in a process
// of its own with the session's environment, which does not hold the
// token: the hook reads it from the session's file. So the hook, run as
// the agent runs it, is taken, and refused when it cannot read the file.
func TestEveryAgentsHooksProveTheirSession(t *testing.T) {
	hookProgram := writeHookProgram(t)
	var hooked int
	for _, driver := range agents.All() {
		if _, ok := driver.(agent.Hooks); !ok {
			continue
		}
		hooked++
		info := driver.Info()
		t.Run(info.Name, func(t *testing.T) {
			outDir, home := t.TempDir(), t.TempDir()
			command := writeHookedAgent(t, info.Programs[0], outDir)
			cfg := &config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{info.Name: {Command: []string{command}, Env: map[string]string{
				"HOME": home, "CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"), "CODEX_HOME": filepath.Join(home, ".codex"),
				"XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".local", "share"),
				"XDG_STATE_HOME": filepath.Join(home, ".local", "state"),
			}}}}
			d := New(cfg, slog.New(slog.DiscardHandler))
			d.mcp = nil
			// A socket's path is short, which a test's temporary directory is not.
			dir, err := os.MkdirTemp("", "mako-auth")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			d.dir = dir
			if err := d.loadSessions(dir); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.sessions.closeAll)
			run, err := config.RunDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			d.hookBinary = hookProgram
			d.listenHooks(t.Context(), run)
			if d.hookCommand == "" {
				t.Fatal("not listening")
			}
			socket := filepath.Join(run, "hooks.sock")
			s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: info.Name, Project: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			env := waitForEnvFile(t, filepath.Join(outDir, s.ID()+".env"))
			if env["MAKO_SESSION"] != s.ID() {
				t.Fatalf("MAKO_SESSION=%q, want %q", env["MAKO_SESSION"], s.ID())
			}
			token := d.hookToken(s.ID())
			args, _ := os.ReadFile(filepath.Join(outDir, s.ID()+".args"))
			values := strings.Join(slices.Collect(maps.Values(env)), "\n")
			if strings.Contains(values, token) || strings.Contains(string(args), token) {
				t.Fatal("the token is in the agent's environment or arguments, which other processes read")
			}
			if got := readHookToken(socket, s.ID()); got != token {
				t.Fatalf("token file holds %q", got)
			}
			// The agent was given the daemon's hook command, in its
			// arguments, its environment, or a file mako wrote for it.
			if given := string(args) + values + filesUnder(run) + filesUnder(filepath.Join(home, ".gemini")); !strings.Contains(given, d.hookCommand) {
				t.Fatalf("no hook command %q given to the agent", d.hookCommand)
			}

			// The hook, as the agent runs it: a shell command of its own,
			// in its environment. agy names its event on the command line.
			hook, input := d.hookCommand, `{"hook_event_name":"Stop"}`
			if info.Name == "agy" {
				hook, input = hook+" Stop", `{}`
			}
			unhide := hideHookToken(t, socket, s.ID())
			runHookCommand(t, hook, env, input)
			waitFor(t, "a hook without the token was not refused", func() bool { return refusedHooks(d) == 1 })
			if s.Info().GetStatus() == protocol.AgentDone {
				t.Fatal("a hook without the token ended the turn")
			}
			unhide()
			runHookCommand(t, hook, env, input)
			waitFor(t, "the agent's own hook was not taken", func() bool { return s.Info().GetStatus() == protocol.AgentDone })
			if refusedHooks(d) != 1 {
				t.Fatal("the agent's own hook was refused")
			}
		})
	}
	if hooked < 5 {
		t.Fatalf("only %d agents hook mako", hooked)
	}
}

// A session in a workspace sandbox that hides the token folder but for
// its own token, as the sandbox hides Mako's run folder, reports for
// itself and for no other session, though it knows the other's ID.
func TestASandboxedSessionReportsForItselfAlone(t *testing.T) {
	const sandboxExec = "/usr/bin/sandbox-exec"
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt is macOS's")
	}
	if _, err := os.Stat(sandboxExec); err != nil {
		t.Skip("no sandbox-exec")
	}
	d, other, socket := hookedDaemon(t)
	boxed, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(socket), hookTokenDir))
	if err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(tokens, filepath.Base(hookTokenFile("", boxed.ID())))
	profile := `(version 1)(allow default)(deny file-read* (subpath "` + tokens + `"))(allow file-read* (literal "` + own + `"))`
	hookProgram := writeHookProgram(t)
	runBoxed := func(session string) {
		t.Helper()
		cmd := exec.Command(sandboxExec, "-p", profile, "--", hookProgram, hookArg, socket)
		cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "MAKO_SESSION=") }), "MAKO_SESSION="+session)
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"Stop"}`)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sandboxed hook: %v: %s", err, out)
		}
	}
	runBoxed(other.ID())
	waitFor(t, "a sandboxed hook for another session was not refused", func() bool { return refusedHooks(d) == 1 })
	if other.Info().GetStatus() == protocol.AgentDone {
		t.Fatal("a sandboxed session ended another's turn")
	}
	runBoxed(boxed.ID())
	waitFor(t, "a sandboxed session's own hook was not taken", func() bool { return boxed.Info().GetStatus() == protocol.AgentDone })
	if refusedHooks(d) != 1 || other.Info().GetStatus() == protocol.AgentDone {
		t.Fatal("a sandboxed session's own hook was refused, or taken as another's")
	}
}
