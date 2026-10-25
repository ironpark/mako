package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

func TestHookReportsFollowTheConversation(t *testing.T) {
	command, root := fakeAgent(t, "claude")
	cfg := &config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{"claude": {Command: []string{command}, Env: map[string]string{"SANBO_FAKE_AGENT": "claude", "SANBO_FAKE_ROOT": root, "CLAUDE_CONFIG_DIR": root}}}}
	d := New(cfg, nil)
	d.mcp = nil
	if err := d.loadSessions(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer d.sessions.closeAll()
	// A socket's path is short, which a test's temporary directory is not.
	run, err := os.MkdirTemp("", "sanbo-hooks")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(run)
	d.hookBinary = "/opt/sanbo's/sanbod"
	d.listenHooks(t.Context(), run)
	socket := filepath.Join(run, "hooks-0.sock")

	project := t.TempDir()
	s, err := d.openSession(t.Context(), protocol.OpenSession{Agent: "claude", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	waitConversation(t, s) // found from its open log before any report
	first, firstPath := s.Info().ConversationID, s.TranscriptPath()
	var args []string
	data, _ := os.ReadFile(filepath.Join(root, "args.json"))
	_ = json.Unmarshal(data, &args)
	var settings struct {
		Hooks map[string][]struct{ Hooks []struct{ Command string } }
	}
	if i := slices.Index(args, "--settings"); i < 0 || json.Unmarshal([]byte(args[i+1]), &settings) != nil {
		t.Fatalf("launch has no hook: %q", args)
	}
	if h := settings.Hooks["SessionStart"]; len(h) != 1 || h[0].Hooks[0].Command != `'/opt/sanbo'\''s/sanbod' hook `+socket {
		t.Fatalf("hook %+v", settings)
	}

	// /clear: a new conversation, whose log the agent writes only later.
	cleared := conversationUUID()
	path := filepath.Join(root, "projects", "project", cleared+".jsonl")
	t.Setenv("SANBO_SESSION", s.ID())
	input, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "source": "clear", "session_id": cleared, "transcript_path": path})
	runHook([]string{socket}, strings.NewReader(string(input)))
	deadline := time.Now().Add(5 * time.Second)
	for s.Info().ConversationID != cleared {
		if time.Now().After(deadline) {
			t.Fatal("hook report not bound")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.TranscriptPath() != "" {
		t.Fatalf("unwritten log bound: %s", s.TranscriptPath())
	}

	// A report that ran earlier but arrives late changes nothing.
	report := func(seq int64, id, path string) error {
		input, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "session_id": id, "transcript_path": path})
		return d.reportConversation(hookMessage{Session: s.ID(), Seq: seq, Input: input})
	}
	if err := report(1, first, firstPath); err != nil {
		t.Fatal(err)
	}
	if s.Info().ConversationID != cleared {
		t.Fatal("a late report replaced a newer one")
	}
	// Nor does a log outside the agent's own place.
	if err := report(time.Now().UnixNano(), cleared, filepath.Join(project, cleared+".jsonl")); err == nil {
		t.Fatal("foreign log accepted")
	}

	// The log is bound once the agent writes it, though the agent still
	// has its first log open.
	record, _ := json.Marshal(map[string]any{"type": "user", "sessionId": cleared, "cwd": project, "timestamp": time.Now(), "message": map[string]any{"role": "user", "content": "after clear"}})
	if err := os.WriteFile(path, append(record, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	waitConversation(t, s)
	if s.TranscriptPath() != path || s.Info().ConversationID != cleared {
		t.Fatalf("bound %s %s", s.Info().ConversationID, s.TranscriptPath())
	}
	d.sessionMu.Lock()
	saved := d.saved[s.ID()]
	d.sessionMu.Unlock()
	if saved.Info.ConversationID != cleared || saved.TranscriptPath != path {
		t.Fatalf("saved %+v", saved)
	}
}
