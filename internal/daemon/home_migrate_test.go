//go:build unix

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

func TestMigrateAgentHomeClaudeAndReverse(t *testing.T) {
	realHome := t.TempDir()
	makoHome := t.TempDir()
	t.Setenv("HOME", realHome)
	t.Setenv("MAKO_HOME", makoHome)
	// Provide a stub `claude` binary on PATH so this test does not require a
	// real Claude CLI installation (e.g. on CI runners).
	stubBin := t.TempDir()
	stubClaude := filepath.Join(stubBin, "claude")
	if err := os.WriteFile(stubClaude, []byte("#!/bin/sh\ncase \"$1\" in --help|-h|plugin|mcp) exit 1 ;; esac\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	claudeDefault := filepath.Join(realHome, ".claude")
	claudeProj := filepath.Join(claudeDefault, "projects", "-w-app")
	if err := os.MkdirAll(claudeProj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDefault, "settings.json"), []byte(`{"model":"opus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDefault, ".credentials.json"), []byte(`{"token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	catDir := filepath.Join(claudeDefault, "cache", "model-catalog")
	if err := os.MkdirAll(catDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catDir, "x-cc.json"), []byte(`{"catalog":{"config":{"models":[{"id":"claude-opus-5-5","name":"Opus 5.5"}]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	makoTranscript := filepath.Join(claudeProj, "conv-mako.jsonl")
	makoSubagent := filepath.Join(claudeProj, "conv-mako", "subagents", "agent-1.jsonl")
	externalTranscript := filepath.Join(claudeProj, "conv-external.jsonl")
	if err := os.WriteFile(makoTranscript, []byte("{\"type\":\"user\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(makoSubagent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(makoSubagent, []byte("{\"type\":\"assistant\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(externalTranscript, []byte("{\"type\":\"user\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := meshDaemon(t, "migrate")
	d.sessionPath = filepath.Join(t.TempDir(), "sessions.json")

	// Add one running claude session and one stopped saved session.
	liveSess, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	d.sessionMu.Lock()
	d.saved["saved-1"] = savedSession{
		Info:           &protocol.SessionInfo{Id: "saved-1", Agent: "claude", ConversationId: "conv-mako", State: protocol.StateStopped},
		Request:        &protocol.OpenSession{Agent: "claude"},
		TranscriptPath: makoTranscript,
	}
	_ = d.saveSessionsLocked()
	d.sessionMu.Unlock()

	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	// Running session is reported and blocks migration.
	info, err := c.InspectAgentHome(ctx, &protocol.InspectAgentHome{Agent: "claude", Isolate: true})
	if err != nil {
		t.Fatal(err)
	}
	if !info.GetSupported() || info.GetRunningSessions() != 1 || !info.GetHasConfig() {
		t.Fatalf("unexpected inspect info: %+v", info)
	}
	if _, err := c.MigrateAgentHome(ctx, &protocol.MigrateAgentHome{Agent: "claude", Isolate: true, CopyConfig: true, MoveSessions: true}); err == nil {
		t.Fatal("expected migration to fail while session is running")
	}

	// Stop/close the running session so only saved-1 remains.
	if err := d.closeSession(liveSess.ID()); err != nil {
		t.Fatal(err)
	}

	res, err := c.MigrateAgentHome(ctx, &protocol.MigrateAgentHome{
		Agent: "claude", Isolate: true, CopyConfig: true, MoveSessions: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetCopiedConfig() || res.GetMovedSessions() != 1 {
		t.Fatalf("unexpected migrate result: %+v", res)
	}

	isoRoot := filepath.Join(makoHome, "home", "claude")
	// Config copied and preserved in original.
	if _, err := os.Stat(filepath.Join(claudeDefault, "settings.json")); err != nil {
		t.Fatalf("original settings.json should be kept: %v", err)
	}
	if credInfo, err := os.Stat(filepath.Join(isoRoot, ".credentials.json")); err != nil || credInfo.Mode().Perm() != 0o600 {
		t.Fatalf("isolated credentials missing or wrong mode: %v %v", credInfo, err)
	}
	if _, err := os.Stat(filepath.Join(isoRoot, "cache", "model-catalog", "x-cc.json")); err != nil {
		t.Fatalf("model catalog cache was not copied: %v", err)
	}

	// Mako session + subagents moved; external session stayed in ~/.claude.
	wantMovedTranscript := filepath.Join(isoRoot, "projects", "-w-app", "conv-mako.jsonl")
	if _, err := os.Stat(wantMovedTranscript); err != nil {
		t.Fatalf("moved transcript missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(isoRoot, "projects", "-w-app", "conv-mako", "subagents", "agent-1.jsonl")); err != nil {
		t.Fatalf("moved subagent log missing: %v", err)
	}
	if _, err := os.Stat(makoTranscript); !os.IsNotExist(err) {
		t.Fatalf("original mako transcript still exists: %v", err)
	}
	if _, err := os.Stat(externalTranscript); err != nil {
		t.Fatalf("external transcript should NOT be moved: %v", err)
	}

	d.sessionMu.Lock()
	updatedPath := d.saved["saved-1"].TranscriptPath
	d.sessionMu.Unlock()
	if updatedPath != wantMovedTranscript {
		t.Fatalf("saved TranscriptPath = %q, want %q", updatedPath, wantMovedTranscript)
	}

	// Reverse migration back to default home.
	rev, err := c.MigrateAgentHome(ctx, &protocol.MigrateAgentHome{
		Agent: "claude", Isolate: false, CopyConfig: false, MoveSessions: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rev.GetMovedSessions() != 1 {
		t.Fatalf("reverse moved = %d, want 1", rev.GetMovedSessions())
	}
	if _, err := os.Stat(makoTranscript); err != nil {
		t.Fatalf("transcript should be back at %s: %v", makoTranscript, err)
	}
	d.sessionMu.Lock()
	restoredPath := d.saved["saved-1"].TranscriptPath
	d.sessionMu.Unlock()
	if restoredPath != makoTranscript {
		t.Fatalf("restored TranscriptPath = %q, want %q", restoredPath, makoTranscript)
	}
}

func TestMigrateAgentHomeAgyHistoryAndSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agy HOME isolation is unix-only")
	}
	realHome := t.TempDir()
	makoHome := t.TempDir()
	t.Setenv("HOME", realHome)
	t.Setenv("MAKO_HOME", makoHome)

	if err := os.WriteFile(filepath.Join(realHome, ".gitconfig"), []byte("[user]\nname = Mako\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agyDefault := filepath.Join(realHome, ".gemini", "antigravity-cli")
	agyCfgDir := filepath.Join(realHome, ".gemini", "config")
	if err := os.MkdirAll(agyDefault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agyCfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agyDefault, "settings.json"), []byte(`{"theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agyCfgDir, "hooks.json"), []byte(`{"user":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	makoLog := filepath.Join(agyDefault, "brain", "conv-1", ".system_generated", "logs", "transcript_full.jsonl")
	extLog := filepath.Join(agyDefault, "brain", "conv-ext", ".system_generated", "logs", "transcript_full.jsonl")
	for _, p := range []string{makoLog, extLog} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"step_index":0}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	historyBody := `{"display":"external","conversationId":"conv-ext"}` + "\n" +
		`{"display":"mako task","conversationId":"conv-1"}` + "\n"
	if err := os.WriteFile(filepath.Join(agyDefault, "history.jsonl"), []byte(historyBody), 0o644); err != nil {
		t.Fatal(err)
	}

	d := meshDaemon(t, "agy-mig")
	d.sessionPath = filepath.Join(t.TempDir(), "sessions.json")
	d.sessionMu.Lock()
	d.saved["s-agy"] = savedSession{
		Info:           &protocol.SessionInfo{Id: "s-agy", Agent: "agy", ConversationId: "conv-1", State: protocol.StateStopped},
		Request:        &protocol.OpenSession{Agent: "agy"},
		TranscriptPath: makoLog,
	}
	_ = d.saveSessionsLocked()
	d.sessionMu.Unlock()

	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	res, err := c.MigrateAgentHome(ctx, &protocol.MigrateAgentHome{
		Agent: "agy", Isolate: true, CopyConfig: true, MoveSessions: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetCopiedConfig() || res.GetMovedSessions() != 1 {
		t.Fatalf("unexpected migrate result: %+v", res)
	}

	isoHome := filepath.Join(makoHome, "home", "agy")
	isoData := filepath.Join(isoHome, ".gemini", "antigravity-cli")
	// Symlink to .gitconfig exists.
	if target, err := os.Readlink(filepath.Join(isoHome, ".gitconfig")); err != nil || target != filepath.Join(realHome, ".gitconfig") {
		t.Fatalf(".gitconfig symlink = %q err=%v", target, err)
	}
	// Config & hooks copied.
	if _, err := os.Stat(filepath.Join(isoData, "settings.json")); err != nil {
		t.Fatalf("settings.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(isoHome, ".gemini", "config", "hooks.json")); err != nil {
		t.Fatalf("hooks.json missing: %v", err)
	}
	// Mako brain moved; external brain kept.
	wantLog := filepath.Join(isoData, "brain", "conv-1", ".system_generated", "logs", "transcript_full.jsonl")
	if _, err := os.Stat(wantLog); err != nil {
		t.Fatalf("moved agy transcript missing: %v", err)
	}
	if _, err := os.Stat(extLog); err != nil {
		t.Fatalf("external agy transcript should stay: %v", err)
	}
	// history.jsonl split accurately.
	srcHist, _ := os.ReadFile(filepath.Join(agyDefault, "history.jsonl"))
	dstHist, _ := os.ReadFile(filepath.Join(isoData, "history.jsonl"))
	if strings.Contains(string(srcHist), "conv-1") || !strings.Contains(string(srcHist), "conv-ext") {
		t.Fatalf("unexpected source history.jsonl: %s", srcHist)
	}
	if !strings.Contains(string(dstHist), "conv-1") || strings.Contains(string(dstHist), "conv-ext") {
		t.Fatalf("unexpected target history.jsonl: %s", dstHist)
	}
}

func TestListAgentModelsRPC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MAKO_HOME", t.TempDir())

	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-6.2-sol","display_name":"GPT-6.2-Sol","visibility":"list","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	d := meshDaemon(t, "models")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	got, err := c.ListAgentModels(ctx, &protocol.ListAgentModels{Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.GetModels()) != 1 || got.GetModels()[0].GetId() != "gpt-6.2-sol" {
		t.Fatalf("unexpected codex models: %+v", got.GetModels())
	}
}

func TestMigrateGlobalHomeMigratesAllAgentsTogether(t *testing.T) {
	realHome := t.TempDir()
	makoHome := t.TempDir()
	t.Setenv("HOME", realHome)
	t.Setenv("MAKO_HOME", makoHome)

	claudeProj := filepath.Join(realHome, ".claude", "projects", "-w-app")
	codexSessDir := filepath.Join(realHome, ".codex", "sessions", "2026", "11", "19")
	for _, dir := range []string{claudeProj, codexSessDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(realHome, ".claude", "settings.json"), []byte(`{}`), 0o644)
	_ = os.WriteFile(filepath.Join(realHome, ".codex", "config.toml"), []byte("model = \"gpt-6.2-sol\"\n"), 0o644)
	claudeLog := filepath.Join(claudeProj, "c-1.jsonl")
	codexLog := filepath.Join(codexSessDir, "rollout-2026-c-2.jsonl")
	_ = os.WriteFile(claudeLog, []byte("{}\n"), 0o644)
	_ = os.WriteFile(codexLog, []byte("{}\n"), 0o644)

	d := meshDaemon(t, "global-mig")
	d.sessionPath = filepath.Join(t.TempDir(), "sessions.json")
	d.sessionMu.Lock()
	d.saved["s1"] = savedSession{
		Info:           &protocol.SessionInfo{Id: "s1", Agent: "claude", ConversationId: "c-1", State: protocol.StateStopped},
		Request:        &protocol.OpenSession{Agent: "claude"},
		TranscriptPath: claudeLog,
	}
	d.saved["s2"] = savedSession{
		Info:           &protocol.SessionInfo{Id: "s2", Agent: "codex", ConversationId: "c-2", State: protocol.StateStopped},
		Request:        &protocol.OpenSession{Agent: "codex"},
		TranscriptPath: codexLog,
	}
	_ = d.saveSessionsLocked()
	d.sessionMu.Unlock()

	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	// Empty agent inspects globally across all isolatable agents.
	info, err := c.InspectAgentHome(ctx, &protocol.InspectAgentHome{Isolate: true})
	if err != nil {
		t.Fatal(err)
	}
	if !info.GetSupported() || info.GetSavedSessions() != 2 || !info.GetHasConfig() {
		t.Fatalf("unexpected global inspect: %+v", info)
	}
	res, err := c.MigrateAgentHome(ctx, &protocol.MigrateAgentHome{Isolate: true, CopyConfig: true, MoveSessions: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetCopiedConfig() || res.GetMovedSessions() != 2 {
		t.Fatalf("unexpected global migrate: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(makoHome, "home", "claude", "projects", "-w-app", "c-1.jsonl")); err != nil {
		t.Fatalf("claude session not moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(makoHome, "home", "codex", "sessions", "2026", "11", "19", "rollout-2026-c-2.jsonl")); err != nil {
		t.Fatalf("codex session not moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(makoHome, "home", "codex", "config.toml")); err != nil {
		t.Fatalf("codex config not copied: %v", err)
	}
}
