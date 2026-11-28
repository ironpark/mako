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
