//go:build unix

package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func waitForEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), "__ENV_DONE__") {
			out := map[string]string{}
			for line := range strings.Lines(string(data)) {
				line = strings.TrimRight(line, "\r\n")
				if line == "__ENV_DONE__" {
					break
				}
				if k, v, ok := strings.Cut(line, "="); ok {
					out[k] = v
				}
			}
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("timed out waiting for env capture at %s (got %q)", path, string(data))
	return nil
}

// writeEnvCaptureAgent creates a fake agent binary named name that responds
// cleanly to daemon feature probes (--help, features, app-server, plugin) and
// writes its actual session launch environment to outDir/<MAKO_SESSION>.env.
func writeEnvCaptureAgent(t *testing.T, name, outDir string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	script := `#!/bin/sh
case "$1" in
  --help|features|app-server|plugin) exit 0 ;;
esac
out="` + outDir + `/${MAKO_SESSION}.env"
env > "${out}.tmp"
printf '__ENV_DONE__\n' >> "${out}.tmp"
mv "${out}.tmp" "${out}"
exec sleep 60
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestOpenSessionHTTPMCPProxyBypassClaudeAndCodex(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	t.Setenv("NO_PROXY", "corp.internal,127.0.0.1")
	t.Setenv("no_proxy", "extra.local,corp.internal")

	outDir := t.TempDir()
	claudeBin := writeEnvCaptureAgent(t, "claude", outDir)
	codexBin := writeEnvCaptureAgent(t, "codex", outDir)

	cfg := &config.Config{
		NodeID: "node",
		Agents: map[string]agentspec.Agent{
			"claude": {Command: []string{claudeBin}},
			"codex":  {Command: []string{codexBin}},
		},
	}
	d := New(cfg, slog.New(slog.DiscardHandler))
	d.dir, d.ctx, d.mcp, d.hookBinary = t.TempDir(), t.Context(), nil, "/bin/makod"
	if err := d.loadSessions(d.dir); err != nil {
		t.Fatal(err)
	}
	if err := d.projectAgents.messages.load(d.dir); err != nil {
		t.Fatal(err)
	}
	run, err := config.RunDir(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.listenAgentMCP(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)

	// 1. Claude session receives local HTTP MCP server and merged NO_PROXY + no_proxy.
	sClaude, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer sClaude.Close()
	gotClaude := waitForEnvFile(t, filepath.Join(outDir, sClaude.ID()+".env"))
	wantMerged := "corp.internal,127.0.0.1,extra.local,localhost,::1"
	if gotClaude["NO_PROXY"] != wantMerged || gotClaude["no_proxy"] != wantMerged {
		t.Fatalf("claude proxy env: NO_PROXY=%q no_proxy=%q, want %q", gotClaude["NO_PROXY"], gotClaude["no_proxy"], wantMerged)
	}

	// 2. Codex session receives local HTTP MCP server and merged NO_PROXY + no_proxy.
	sCodex, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "codex", Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer sCodex.Close()
	gotCodex := waitForEnvFile(t, filepath.Join(outDir, sCodex.ID()+".env"))
	if gotCodex["NO_PROXY"] != wantMerged || gotCodex["no_proxy"] != wantMerged {
		t.Fatalf("codex proxy env: NO_PROXY=%q no_proxy=%q, want %q", gotCodex["NO_PROXY"], gotCodex["no_proxy"], wantMerged)
	}

	// 3. Explicit OpenSession.Env NO_PROXY override wins over ambient while still adding loopback hosts.
	sOverride, err := d.openSession(t.Context(), &protocol.OpenSession{
		Agent:   "claude",
		Project: t.TempDir(),
		Env:     map[string]string{"NO_PROXY": "custom.only"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sOverride.Close()
	gotOverride := waitForEnvFile(t, filepath.Join(outDir, sOverride.ID()+".env"))
	wantOverride := "custom.only,127.0.0.1,localhost,::1"
	if gotOverride["NO_PROXY"] != wantOverride || gotOverride["no_proxy"] != wantOverride {
		t.Fatalf("override proxy env: NO_PROXY=%q no_proxy=%q, want %q", gotOverride["NO_PROXY"], gotOverride["no_proxy"], wantOverride)
	}
}

func TestOpenSessionSanitizesAmbientSessionEnvAndPreservesExplicitOverrides(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", "leaked-thread-id")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "leaked-claude-session")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "sdk-cli")
	t.Setenv("CLAUDE_CODE_SSE_PORT", "43210")
	t.Setenv("ANTIGRAVITY_AGENT", "1")
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "leaked-agy-conv")
	t.Setenv("ANTIGRAVITY_LS_ADDRESS", "127.0.0.1:45678")
	t.Setenv("MAKO_SESSION", "leaked-mako-session")
	t.Setenv("MAKO_SCOPE", "leaked-mako-scope")
	t.Setenv("MAKO_LIVE_SOCKET", "/tmp/leaked-live.sock")
	t.Setenv("MAKO_LIVE_TOKEN", "leaked-live-token")
	t.Setenv("MAKO_AGENT_MCP_TOKEN", "leaked-mcp-token")
	t.Setenv("MAKO_MCP_TOKEN_STALE", "leaked-bearer")
	t.Setenv("MAKO_CODEX_WS_TOKEN", "leaked-ws-token")
	t.Setenv("AGENT_MAKO_SESSION", "leaked-agent-session")
	t.Setenv("AGENT_MAKO_PARENT", "leaked-parent")
	t.Setenv("AGENT_MAKO_PARENT_NODE", "leaked-parent-node")
	t.Setenv("AGENT_MAKO_PROFILE", "leaked-profile")
	t.Setenv("AGENT_MAKO_SOCKET", "/tmp/leaked-cli.sock")
	t.Setenv("MAKO_USER_KEEP_ENV", "still-here")

	outDir := t.TempDir()
	claudeBin := writeEnvCaptureAgent(t, "claude", outDir)

	cfg := &config.Config{
		NodeID: "node-san",
		Agents: map[string]agentspec.Agent{
			"claude": {Command: []string{claudeBin}},
		},
	}
	d := New(cfg, slog.New(slog.DiscardHandler))
	d.dir, d.ctx, d.mcp, d.hookBinary = t.TempDir(), t.Context(), nil, ""
	if err := d.loadSessions(d.dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)

	proj := t.TempDir()
	s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: proj})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := waitForEnvFile(t, filepath.Join(outDir, s.ID()+".env"))
	if got["MAKO_USER_KEEP_ENV"] != "still-here" {
		t.Fatalf("non-session ambient env lost: %v", got["MAKO_USER_KEEP_ENV"])
	}
	if got["MAKO_SESSION"] != s.ID() {
		t.Fatalf("MAKO_SESSION = %q, want %q", got["MAKO_SESSION"], s.ID())
	}
	if got["MAKO_SCOPE"] == "" || got["MAKO_SCOPE"] == "leaked-mako-scope" {
		t.Fatalf("MAKO_SCOPE = %q, want fresh scope", got["MAKO_SCOPE"])
	}
	if got["AGENT_MAKO_SESSION"] != s.ID() || got["AGENT_MAKO_MODE"] != "main" || got["AGENT_MAKO_NODE"] != "node-san" {
		t.Fatalf("AGENT_MAKO_* not set cleanly: session=%q mode=%q node=%q", got["AGENT_MAKO_SESSION"], got["AGENT_MAKO_MODE"], got["AGENT_MAKO_NODE"])
	}

	for _, gone := range []string{
		"CODEX_THREAD_ID",
		"CLAUDECODE",
		"CLAUDE_CODE_SESSION_ID",
		"CLAUDE_CODE_ENTRYPOINT",
		"CLAUDE_CODE_SSE_PORT",
		"ANTIGRAVITY_AGENT",
		"ANTIGRAVITY_CONVERSATION_ID",
		"ANTIGRAVITY_LS_ADDRESS",
		"MAKO_LIVE_SOCKET",
		"MAKO_LIVE_TOKEN",
		"MAKO_AGENT_MCP_TOKEN",
		"MAKO_MCP_TOKEN_STALE",
		"MAKO_CODEX_WS_TOKEN",
		"AGENT_MAKO_PARENT",
		"AGENT_MAKO_PARENT_NODE",
		"AGENT_MAKO_PROFILE",
		"AGENT_MAKO_SOCKET",
	} {
		if v, exists := got[gone]; exists {
			t.Errorf("leaked session-scoped env %s=%q", gone, v)
		}
	}

	// Explicit OpenSession.Env overrides win even for session-scoped keys.
	s2, err := d.openSession(t.Context(), &protocol.OpenSession{
		Agent:   "claude",
		Project: proj,
		Env: map[string]string{
			"CODEX_THREAD_ID":  "explicit-thread-123",
			"MAKO_LIVE_TOKEN":  "explicit-live-tok",
			"USER_LAUNCH_FLAG": "enabled",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got2 := waitForEnvFile(t, filepath.Join(outDir, s2.ID()+".env"))
	if got2["CODEX_THREAD_ID"] != "explicit-thread-123" {
		t.Fatalf("explicit CODEX_THREAD_ID override = %q, want explicit-thread-123", got2["CODEX_THREAD_ID"])
	}
	if got2["MAKO_LIVE_TOKEN"] != "explicit-live-tok" {
		t.Fatalf("explicit MAKO_LIVE_TOKEN override = %q, want explicit-live-tok", got2["MAKO_LIVE_TOKEN"])
	}
	if got2["USER_LAUNCH_FLAG"] != "enabled" {
		t.Fatalf("USER_LAUNCH_FLAG = %q, want enabled", got2["USER_LAUNCH_FLAG"])
	}
	if _, leaked := got2["CLAUDECODE"]; leaked {
		t.Fatalf("CLAUDECODE leaked when not overridden")
	}
}

func TestMergeEnvSliceSanitizesAmbientForCodexAppServer(t *testing.T) {
	base := []string{
		"HOME=/home/user",
		"CODEX_THREAD_ID=stale-thread",
		"CLAUDECODE=1",
		"MAKO_SESSION=stale-session",
		"MAKO_MCP_TOKEN_OLD=stale-token",
	}
	got := mergeEnvSlice(base, map[string]string{
		"MAKO_SESSION":        "fresh-session",
		"MAKO_MCP_TOKEN_MAKO": "fresh-token",
		"NO_PROXY":            "127.0.0.1,localhost,::1",
		"no_proxy":            "127.0.0.1,localhost,::1",
	})
	for _, want := range []string{
		"HOME=/home/user",
		"MAKO_SESSION=fresh-session",
		"MAKO_MCP_TOKEN_MAKO=fresh-token",
		"NO_PROXY=127.0.0.1,localhost,::1",
		"no_proxy=127.0.0.1,localhost,::1",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("mergeEnvSlice missing %q in %q", want, got)
		}
	}
	for _, gone := range []string{"CODEX_THREAD_ID=", "CLAUDECODE=", "MAKO_SESSION=stale-session", "MAKO_MCP_TOKEN_OLD="} {
		for _, kv := range got {
			if strings.HasPrefix(kv, gone) {
				t.Errorf("mergeEnvSlice leaked %q", kv)
			}
		}
	}
}
