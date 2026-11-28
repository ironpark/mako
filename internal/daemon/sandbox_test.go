//go:build unix

package daemon

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/sandbox"
)

// sandboxDaemon is a daemon whose claude agent, a script that reports what
// it could reach to result.<session> in its project, runs in the
// workspace sandbox; its "open" profile turns the sandbox off.
func sandboxDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	home, makoHome := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MAKO_HOME", makoHome)
	dir, project, binDir := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "peer.key"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeClaude := filepath.Join(binDir, "claude")
	script := `#!/bin/sh
try() { if eval "$1" >/dev/null 2>&1; then echo "$2=yes"; else echo "$2=no"; fi; }
{
  echo "tmpdir=$TMPDIR"
  echo "home=$CLAUDE_CONFIG_DIR"
  try 'echo x > "$MAKO_TEST_DIR/config.json"' config-write
  try 'cat "$MAKO_TEST_DIR/peer.key"' key-read
  try 'echo "{}" > "$CLAUDE_CONFIG_DIR/settings.json"' home-write
  try 'echo x > "$TMPDIR/x"' temp-write
  try 'mkdir -p .claude && echo "{}" > .claude/settings.json' control-write
  try 'echo x > notes.txt' project-write
} > "$PWD/result.$AGENT_MAKO_SESSION"
sleep 30
`
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{
		"claude": {Command: []string{fakeClaude}, Sandbox: agentspec.SandboxWorkspace,
			Env:      map[string]string{"MAKO_TEST_DIR": dir},
			Profiles: []agentspec.Profile{{Name: "open", Sandbox: agentspec.SandboxOff}}},
	}}
	d := New(cfg, slog.New(slog.DiscardHandler))
	d.dir, d.ctx, d.mcp, d.hookBinary = dir, t.Context(), nil, "/bin/makod"
	if err := d.loadSessions(d.dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)
	return d, project
}

// sandboxResult waits for session id's report and returns it.
func sandboxResult(t *testing.T, project, id string) map[string]string {
	t.Helper()
	path := filepath.Join(project, "result."+id)
	waitForWithin(t, 10*time.Second, "the agent did not report", func() bool {
		b, _ := os.ReadFile(path)
		return strings.Contains(string(b), "project-write=")
	})
	b, _ := os.ReadFile(path)
	out := map[string]string{}
	for line := range strings.Lines(string(b)) {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		out[k] = v
	}
	return out
}

func TestSandboxedSessionsKeepToTheirWorkspace(t *testing.T) {
	if err := sandbox.Supported(); err != nil {
		t.Skip(err)
	}
	if exec.Command("/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true").Run() != nil {
		t.Skip("sandbox-exec cannot apply a profile here, as inside another sandbox")
	}
	d, project := sandboxDaemon(t)
	parent, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	got := sandboxResult(t, project, parent.ID())
	home := filepath.Join(os.Getenv("MAKO_HOME"), "sandbox", "claude")
	want := map[string]string{"home": home, "config-write": "no", "key-read": "no", "home-write": "yes",
		"temp-write": "yes", "control-write": "no", "project-write": "yes"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (report %v)", k, got[k], v, got)
		}
	}
	if !strings.Contains(got["tmpdir"], "mako-sandbox-"+parent.ID()) {
		t.Errorf("TMPDIR = %q, not the session's own", got["tmpdir"])
	}
	if b, _ := os.ReadFile(filepath.Join(d.dir, "config.json")); len(b) != 0 {
		t.Fatalf("the sandboxed agent wrote Mako's configuration: %q", b)
	}

	// What it starts runs in the sandbox, though its profile has none.
	child, err := d.startSessionAs(t.Context(), newSessionID(), &protocol.OpenSession{Agent: "claude", Profile: "open", Project: project}, parent.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := sandboxResult(t, project, child.ID()); got["config-write"] != "no" || got["control-write"] != "no" {
		t.Fatalf("the child of a sandboxed session ran outside the sandbox: %v", got)
	}
	d.sessionMu.Lock()
	parentBox, childBox := d.saved[parent.ID()].Sandbox, d.saved[child.ID()].Sandbox
	d.sessionMu.Unlock()
	if parentBox != agentspec.SandboxWorkspace || childBox != agentspec.SandboxWorkspace {
		t.Fatalf("saved sandboxes = %q, %q", parentBox, childBox)
	}

	// A session of the open profile, started by the user, does not.
	open, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Profile: "open", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if got := sandboxResult(t, project, open.ID()); got["control-write"] != "yes" || got["home"] == home {
		t.Fatalf("the open profile ran in the sandbox: %v", got)
	}
}

func TestSandboxedSessionsRefuseWhereTheSandboxIsNotSafe(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS has the sandbox")
	}
	d, project := sandboxDaemon(t)
	_, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: project})
	if !errors.Is(err, sandbox.ErrUnsupported) {
		t.Fatalf("openSession = %v, want the sandbox's refusal", err)
	}
	d.sessionMu.Lock()
	n := len(d.saved)
	d.sessionMu.Unlock()
	if n != 0 {
		t.Fatalf("a refused session was saved")
	}
}

func TestASandboxedSessionStaysInItsSandbox(t *testing.T) {
	d, project := sandboxDaemon(t)
	d.sessionMu.Lock()
	d.saved["boxed"] = savedSession{Info: &protocol.SessionInfo{Id: "boxed", Agent: "claude"}, Request: &protocol.OpenSession{Agent: "claude", Project: project}, Sandbox: agentspec.SandboxWorkspace}
	d.sessionMu.Unlock()

	// It keeps its sandbox in sessions.json.
	raw, err := json.Marshal(d.saved["boxed"])
	if err != nil {
		t.Fatal(err)
	}
	var back savedSession
	if err := json.Unmarshal(raw, &back); err != nil || back.Sandbox != agentspec.SandboxWorkspace {
		t.Fatalf("saved sandbox = %q, %v, from %s", back.Sandbox, err, raw)
	}

	// It reaches no other node.
	if _, err := d.startDirectSSHSession(t.Context(), newSessionID(), &protocol.OpenSession{Agent: "claude", NodeId: "ssh:h"}, "boxed"); !errors.Is(err, errSandboxRemote) {
		t.Fatalf("an SSH session of a sandboxed one = %v", err)
	}
	if _, err := d.startDirectSSHSession(t.Context(), newSessionID(), &protocol.OpenSession{Agent: "claude", NodeId: "ssh:h"}, ""); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("a sandboxed agent on an SSH host = %v", err)
	}
}

func TestTheSandboxHidesTheConfigurationFolderRunUses(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	d := New(&config.Config{NodeID: "node"}, slog.New(slog.DiscardHandler))
	if got, _ := d.configDir(); got != os.Getenv("MAKO_HOME") {
		t.Fatalf("configDir = %q, want MAKO_HOME", got)
	}
	d.workspace.dir = t.TempDir()
	if got, _ := d.configDir(); got != d.workspace.dir {
		t.Fatalf("configDir = %q, want the folder Run loaded %q", got, d.workspace.dir)
	}
}

func TestASandboxedSessionReachesOnlyTheSandbox(t *testing.T) {
	d := New(&config.Config{NodeID: "node"}, slog.New(slog.DiscardHandler))
	d.sessionMu.Lock()
	d.saved = map[string]savedSession{
		"parent":  {Info: &protocol.SessionInfo{Id: "parent"}},
		"peer":    {Info: &protocol.SessionInfo{Id: "peer"}},
		"boxed":   {Info: &protocol.SessionInfo{Id: "boxed"}, ParentID: "parent", Sandbox: agentspec.SandboxWorkspace},
		"sibling": {Info: &protocol.SessionInfo{Id: "sibling"}, ParentID: "parent", Sandbox: agentspec.SandboxWorkspace},
	}
	d.sessionMu.Unlock()
	for _, c := range []struct {
		from, to string
		message  bool
		ok       bool
	}{
		{"boxed", "parent", true, true},   // it reports to its parent
		{"boxed", "parent", false, false}, // but does not read it
		{"boxed", "peer", true, false},    // nor tells another what to do
		{"boxed", "peer", false, false},   // nor reads it
		{"boxed", "remote", true, false},  // nor reaches another node's
		{"boxed", "sibling", true, true},  // in the sandbox, it may
		{"boxed", "sibling", false, true}, //
		{"peer", "boxed", true, true},     // and outside it, all may
		{"parent", "peer", false, true},   //
		{"sibling", "boxed", false, true}, //
		{"boxed", "", true, false},        //
	} {
		err := d.sandboxReach(c.from, c.to, c.message)
		if (err == nil) != c.ok {
			t.Errorf("sandboxReach(%s, %s, message %v) = %v, want ok %v", c.from, c.to, c.message, err, c.ok)
		}
	}
}

func TestTheSandboxSpecKeepsWhatAgentsOutsideItRun(t *testing.T) {
	home, makoHome := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MAKO_HOME", "")
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(home, "agent.sock"))
	d := New(&config.Config{NodeID: "node"}, slog.New(slog.DiscardHandler))
	d.dir = makoHome
	run, err := config.RunDir(makoHome)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pi-hooks-0a1b2c.js", "omp-hooks-0a1b2c.js", "omo-hooks-0a1b2c.js", "opencode-mako-0a1b2c.js", sidecarTokenFile} {
		if err := os.WriteFile(filepath.Join(run, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := d.sandboxSpec("s1", "agy", agentspec.SandboxWorkspace, []string{t.TempDir()}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{makoHome, filepath.Join(home, ".mako", "home"), filepath.Join(home, ".mako", "sandbox"), filepath.Join(home, ".ssh")} {
		if !slices.Contains(spec.Hidden, want) {
			t.Errorf("Hidden lacks %s: %q", want, spec.Hidden)
		}
	}
	for _, name := range []string{"pi-hooks-0a1b2c.js", "omp-hooks-0a1b2c.js", "omo-hooks-0a1b2c.js", "opencode-mako-0a1b2c.js"} {
		if !slices.Contains(spec.Readable, filepath.Join(run, name)) {
			t.Errorf("Readable lacks the hook extension %s: %q", name, spec.Readable)
		}
	}
	if slices.Contains(spec.Readable, filepath.Join(run, sidecarTokenFile)) {
		t.Errorf("a sandboxed agy reads the sidecar token: %q", spec.Readable)
	}
	if slices.Contains(spec.Sockets, os.Getenv("SSH_AUTH_SOCK")) || slices.Contains(spec.Readable, filepath.Join(home, ".ssh", "known_hosts")) {
		t.Errorf("the SSH agent is in a sandbox that does not give it: %q, %q", spec.Sockets, spec.Readable)
	}
	ssh, err := d.sandboxSpec("s1", "agy", agentspec.SandboxWorkspaceSSH, []string{t.TempDir()}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ssh.Sockets, os.Getenv("SSH_AUTH_SOCK")) || !slices.Contains(ssh.Readable, filepath.Join(home, ".ssh", "known_hosts")) {
		t.Errorf("the SSH agent is not in the sandbox that gives it: %q, %q", ssh.Sockets, ssh.Readable)
	}
	if slices.Contains(ssh.Readable, filepath.Join(home, ".ssh", "id_ed25519")) {
		t.Errorf("the sandbox reads SSH keys: %q", ssh.Readable)
	}
}

func TestAResumedSessionKeepsTheSandboxItStartedIn(t *testing.T) {
	d, project := sandboxDaemon(t)
	// One that started before its agent ran in the sandbox: its
	// conversation is in the home outside it.
	d.sessionMu.Lock()
	d.saved["old"] = savedSession{Info: &protocol.SessionInfo{Id: "old", Agent: "claude", Project: project}, Request: &protocol.OpenSession{Agent: "claude", Project: project}}
	d.sessionMu.Unlock()
	s, err := d.startSession(t.Context(), "old", &protocol.OpenSession{Agent: "claude", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if got := sandboxResult(t, project, s.ID()); got["control-write"] != "yes" || strings.Contains(got["home"], filepath.Join("sandbox", "claude")) {
		t.Fatalf("a session that started outside the sandbox moved into it: %v", got)
	}
	d.sessionMu.Lock()
	box := d.saved["old"].Sandbox
	d.sessionMu.Unlock()
	if box != "" {
		t.Fatalf("saved sandbox = %q", box)
	}
}

func TestARewindKeepsTheSandboxOfTheSession(t *testing.T) {
	d, project := sandboxDaemon(t)
	// A sandboxed session of the open profile, as a sandboxed parent's
	// child is; rewound to before its first turn, it starts anew.
	d.sessionMu.Lock()
	d.saved["boxed"] = savedSession{
		Info:    &protocol.SessionInfo{Id: "boxed", Agent: "claude", Profile: "open", Project: project},
		Request: &protocol.OpenSession{Agent: "claude", Profile: "open", Project: project},
		Sandbox: agentspec.SandboxWorkspace,
	}
	d.sessionMu.Unlock()
	info, err := d.rewindSession(t.Context(), &protocol.RewindSession{SessionId: "boxed"})
	if runtime.GOOS != "darwin" {
		if !errors.Is(err, sandbox.ErrUnsupported) {
			t.Fatalf("rewind = %v, want the sandbox's refusal, which only a sandboxed start meets", err)
		}
		return
	}
	if exec.Command("/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true").Run() != nil {
		t.Skip("sandbox-exec cannot apply a profile here, as inside another sandbox")
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := sandboxResult(t, project, info.GetId()); got["control-write"] != "no" {
		t.Fatalf("the rewound session ran outside the sandbox: %v", got)
	}
	d.sessionMu.Lock()
	box := d.saved[info.GetId()].Sandbox
	d.sessionMu.Unlock()
	if box != agentspec.SandboxWorkspace {
		t.Fatalf("saved sandbox = %q", box)
	}
}

func TestARefusedSandboxMakesNoHome(t *testing.T) {
	d, _ := sandboxDaemon(t)
	// The home folder as the project, which the sandbox refuses.
	_, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: os.Getenv("HOME")})
	if err == nil {
		t.Fatal("a sandboxed session of the home folder started")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("MAKO_HOME"), "sandbox", "claude")); err == nil {
		t.Fatal("a refused session made its sandbox home")
	}
}

func TestARestartedCodexTUIStaysInTheSandbox(t *testing.T) {
	tui := []string{"codex", "--remote", "unix:///tmp/x.sock", "resume", "t1"}
	if got, err := sandboxedTUI(nil, tui); err != nil || !slices.Equal(got, tui) {
		t.Fatalf("an unsandboxed TUI = %q, %v", got, err)
	}
	box := &sandbox.Spec{Roots: []string{t.TempDir()}, UserHome: t.TempDir()}
	got, err := sandboxedTUI(box, tui)
	if sandbox.Supported() != nil {
		if got != nil || !errors.Is(err, sandbox.ErrUnsupported) {
			t.Fatalf("a sandboxed TUI where there is no sandbox = %q, %v", got, err)
		}
		return
	}
	if err != nil || got[0] != "/usr/bin/sandbox-exec" || !slices.Equal(got[len(got)-len(tui):], tui) {
		t.Fatalf("a restarted sandboxed TUI = %q, %v", got, err)
	}
}

func TestASandboxedSessionReadsItsOwnHookTokenOnly(t *testing.T) {
	makoHome := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAKO_HOME", makoHome)
	d := New(&config.Config{NodeID: "node"}, slog.New(slog.DiscardHandler))
	d.dir = makoHome
	run, err := config.RunDir(makoHome)
	if err != nil {
		t.Fatal(err)
	}
	d.hookRun = run
	if err := os.MkdirAll(filepath.Join(run, hookTokenDir), 0o700); err != nil {
		t.Fatal(err)
	}
	d.hookSecret = []byte("secret")
	for _, id := range []string{"s1", "s2"} {
		if err := d.giveHookToken(id); err != nil {
			t.Fatal(err)
		}
	}
	own, other := hookTokenFile(run, "s1"), hookTokenFile(run, "s2")
	project := t.TempDir()
	spec, err := d.sandboxSpec("s1", "claude", agentspec.SandboxWorkspace, []string{project}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spec.Readable, own) || slices.Contains(spec.Readable, other) || slices.Contains(spec.Readable, filepath.Join(run, hookTokenDir)) {
		t.Fatalf("Readable = %q, want its own token file %s alone", spec.Readable, own)
	}
	if sandbox.Supported() != nil || exec.Command("/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true").Run() != nil {
		return
	}
	spec.Temp = t.TempDir()
	read := func(path string) error {
		argv, err := sandbox.Command(*spec, []string{"/bin/cat", path})
		if err != nil {
			t.Fatal(err)
		}
		return exec.Command(argv[0], argv[1:]...).Run()
	}
	if err := read(own); err != nil {
		t.Errorf("the session cannot read its own hook token: %v", err)
	}
	if err := read(other); err == nil {
		t.Error("the session read another session's hook token")
	}
	if err := os.WriteFile(filepath.Join(run, "state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := read(filepath.Join(run, "state.json")); err == nil {
		t.Error("the session read the run folder")
	}
}
