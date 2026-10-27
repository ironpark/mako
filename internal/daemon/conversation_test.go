package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// This subprocess emulates a native agent, holding its own log open. No API
// calls or installed agent binaries are used by the lifecycle tests.
func TestConversationAgentProcess(t *testing.T) {
	agent := os.Getenv("MAKO_FAKE_AGENT")
	if agent == "" {
		return
	}
	split := slices.Index(os.Args, "--")
	args := os.Args[split+1:]
	if slices.Contains(args, "--help") {
		fmt.Println("--no-daemon")
		os.Exit(0)
	}
	id := ""
	for i, arg := range args {
		if (arg == "--session-id" || arg == "--resume" || arg == "resume") && i+1 < len(args) {
			id = args[i+1]
		}
	}
	if id == "" {
		id = conversationUUID()
	}
	root := os.Getenv("MAKO_FAKE_ROOT")
	path := filepath.Join(root, "projects", "project", id+".jsonl")
	if agent == "codex" {
		path = filepath.Join(root, "sessions", "2026", "10", "26", "rollout-"+id+".jsonl")
	}
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	defer f.Close()
	cwd, _ := os.Getwd()
	var record any = map[string]any{"type": "user", "sessionId": id, "cwd": cwd, "timestamp": time.Now(), "message": map[string]any{"role": "user", "content": "original conversation"}}
	if agent == "codex" {
		record = map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": cwd, "timestamp": time.Now(), "source": "cli"}}
	}
	_ = json.NewEncoder(f).Encode(record)
	_ = f.Sync()
	capture, _ := json.Marshal(args)
	_ = os.WriteFile(filepath.Join(root, "args.json"), capture, 0600)
	_, _ = io.Copy(os.Stdout, os.Stdin)
	os.Exit(0)
}

func fakeAgent(t *testing.T, agent string) (string, string) {
	t.Helper()
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), agent)
	body := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", `'\''`) + "' -test.run=TestConversationAgentProcess -- \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return script, root
}

func waitConversation(t *testing.T, s *session.Session) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for s.TranscriptPath() == "" {
		if time.Now().After(deadline) {
			t.Fatal("native conversation was not identified")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestNativeConversationSurvivesDaemonRestartWithoutOpeningChat(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			command, root := fakeAgent(t, agent)
			cfg := &config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{agent: {Command: []string{command}, Args: []string{"--model", "test-model"}, Env: map[string]string{"MAKO_FAKE_AGENT": agent, "MAKO_FAKE_ROOT": root, "CLAUDE_CONFIG_DIR": root, "CODEX_HOME": root}}}}
			saved, project := t.TempDir(), t.TempDir()
			daemon := func() *Daemon {
				d := New(cfg, nil)
				d.mcp = nil
				if err := d.loadSessions(saved); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(d.sessions.closeAll)
				return d
			}
			first := daemon()
			s, err := first.openSession(t.Context(), &protocol.OpenSession{Agent: agent, Project: project, Args: []string{"initial prompt"}})
			if err != nil {
				t.Fatal(err)
			}
			waitConversation(t, s)
			nativeID := s.Info().GetConversationId()
			if nativeID == "" {
				t.Fatal("missing native ID")
			}
			first.sessions.closeAll()
			next := daemon()
			if got := next.listSessions()[0].GetConversationId(); got != nativeID {
				t.Fatalf("saved ID=%q want %q", got, nativeID)
			}
			resumed, err := next.attachSession(t.Context(), s.ID())
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			var args []string
			for {
				data, _ := os.ReadFile(filepath.Join(root, "args.json"))
				_ = json.Unmarshal(data, &args)
				if slices.Contains(args, nativeID) && (slices.Contains(args, "resume") || slices.Contains(args, "--resume")) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("not resumed: %q", args)
				}
				time.Sleep(20 * time.Millisecond)
			}
			if slices.Contains(args, "initial prompt") || !slices.Contains(args, "test-model") {
				t.Fatalf("resume flags: %q", args)
			}
			chat, err := next.chatForSession(t.Context(), resumed)
			if err != nil || chat.GetConversation() != nativeID {
				t.Fatalf("automatic chat=%+v, %v", chat, err)
			}
			// A missing saved log must not silently become an empty, new conversation.
			next.sessions.closeAll()
			if err := os.Remove(resumed.TranscriptPath()); err != nil {
				t.Fatal(err)
			}
			if _, err := daemon().attachSession(t.Context(), s.ID()); err == nil {
				t.Fatal("missing conversation silently restarted")
			}
		})
	}
}

func TestConcurrentAgentsInSameProjectBindTheirOwnLogs(t *testing.T) {
	command, root := fakeAgent(t, "codex")
	cfg := &config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{"codex": {Command: []string{command}, Env: map[string]string{"MAKO_FAKE_AGENT": "codex", "MAKO_FAKE_ROOT": root, "CODEX_HOME": root}}}}
	d := New(cfg, nil)
	d.mcp = nil
	if err := d.loadSessions(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer d.sessions.closeAll()
	project := t.TempDir()
	a, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "codex", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "codex", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	waitConversation(t, a)
	waitConversation(t, b)
	if a.Info().GetConversationId() == b.Info().GetConversationId() || a.TranscriptPath() == b.TranscriptPath() {
		t.Fatal("concurrent sessions share a conversation")
	}
	for _, s := range []*session.Session{a, b} {
		chat, err := d.chatForSession(t.Context(), s)
		if err != nil || chat.GetConversation() != s.Info().GetConversationId() {
			t.Fatalf("wrong chat: %+v %v", chat, err)
		}
	}
}
