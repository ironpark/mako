package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// A resting agent sleeps: its program ends, the session stays, stopped,
// and attaching it starts the program again on its conversation. One
// attached to, or with no conversation to resume, does not.
func TestSleepingSessionResumesOnAttach(t *testing.T) {
	home, project, bin := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	starts := filepath.Join(project, "starts")
	// A stand-in for claude, as its own program: it answers a probe of
	// its flags, and otherwise notes its start and waits.
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in --help|-h|--version) exit 0;; esac; done\necho \"$*\" >> " + starts + "\nexec cat\n"
	claude := filepath.Join(bin, "claude")
	if err := os.WriteFile(claude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := New(&config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{
		"claude": {Command: []string{claude}},
		"plain":  {Command: []string{"/bin/sh"}},
	}}, nil)
	d.mcp = nil
	if err := d.loadSessions(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)

	plain, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "plain", Project: project, Args: []string{"-c", "exec cat"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.sleepSession(plain.ID()); err == nil || !strings.Contains(err.Error(), "no conversation") {
		t.Fatalf("slept an agent with no conversation to resume: %v", err)
	}

	s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	conversation := s.Info().GetConversationId()
	if conversation == "" {
		t.Fatal("no conversation pinned")
	}
	startsIn(t, starts, 1)
	s.SetTitle("Fix the build") // as its program would set it
	sub := s.Subscribe()
	if _, err := d.sleepSession(s.ID()); err == nil || !strings.Contains(err.Error(), "attached") {
		t.Fatalf("slept a session attached to: %v", err)
	}
	sub.Close()

	info, err := d.sleepSession(s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if info.GetState() != protocol.StateStopped || info.GetConversationId() != conversation {
		t.Fatalf("asleep as %+v", info)
	}
	if _, live := d.sessions.get(s.ID()); live {
		t.Fatal("its program still runs")
	}
	if list := d.listSessions(); !strings.Contains(stateOf(list, s.ID()), string(protocol.StateStopped)) {
		t.Fatalf("listed as %q", stateOf(list, s.ID()))
	}

	again, err := d.attachSession(t.Context(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Info(); got.GetState() != protocol.StateRunning || got.GetConversationId() != conversation || got.GetTitle() != "Fix the build" {
		t.Fatalf("resumed as %+v", got)
	}
	if lines := startsIn(t, starts, 2); !strings.Contains(lines[1], conversation) {
		t.Fatalf("started again with %q", lines)
	}
}

func stateOf(list []*protocol.SessionInfo, id string) string {
	for _, s := range list {
		if s.GetId() == id {
			return s.GetState()
		}
	}
	return ""
}

// startsIn waits for the stand-in's n starts to be noted in file, and
// returns the arguments of each.
func startsIn(t *testing.T, file string, n int) []string {
	t.Helper()
	var lines []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		data, _ := os.ReadFile(file)
		if lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n"); len(data) > 0 && len(lines) >= n {
			return lines
		}
	}
	t.Fatalf("%d starts, not %d: %q", len(lines), n, lines)
	return nil
}
