package session

import (
	"slices"
	"strings"
	"testing"
)

// A session's program sees the terminal it runs in, not the one the daemon
// was started from.
func TestTerminalEnvNamesTheSessionsTerminal(t *testing.T) {
	got := terminalEnv([]string{"HOME=/home/me", "TERM=xterm-kitty", "TERM_PROGRAM=vscode", "TERM_PROGRAM_VERSION=1.99", "LC_TERMINAL=iTerm2"})
	for _, want := range []string{"HOME=/home/me", "TERM=xterm-256color", "TERM_PROGRAM=ghostty", "TERM_PROGRAM_VERSION=" + ghosttyVersion, "COLORTERM=truecolor"} {
		if !slices.Contains(got, want) {
			t.Errorf("env lacks %s: %q", want, got)
		}
	}
	for _, gone := range []string{"TERM=xterm-kitty", "TERM_PROGRAM=vscode", "TERM_PROGRAM_VERSION=1.99", "LC_TERMINAL=iTerm2"} {
		if slices.Contains(got, gone) {
			t.Errorf("env keeps %s", gone)
		}
	}
}

// sessionEnv strips inherited session-scoped provider/Mako variables while
// setting this session's identity and letting explicit overrides win without
// duplicate keys.
func TestSessionEnvSanitizesAndPreservesOverrides(t *testing.T) {
	ambient := []string{
		"HOME=/home/me",
		"PATH=/usr/bin",
		"NO_PROXY=corp.local",
		"no_proxy=corp.local",
		"CODEX_THREAD_ID=stale-thread",
		"CLAUDECODE=1",
		"CLAUDE_CODE_SESSION_ID=stale-claude",
		"CLAUDE_CODE_ENTRYPOINT=sdk-cli",
		"CLAUDE_CODE_SSE_PORT=12345",
		"ANTIGRAVITY_AGENT=1",
		"ANTIGRAVITY_CONVERSATION_ID=stale-agy",
		"ANTIGRAVITY_LS_ADDRESS=127.0.0.1:9999",
		"MAKO_SESSION=stale-session",
		"MAKO_SCOPE=stale-scope",
		"MAKO_LIVE_SOCKET=/tmp/stale.sock",
		"MAKO_LIVE_TOKEN=stale-live-tok",
		"MAKO_AGENT_MCP_TOKEN=stale-mcp-tok",
		"MAKO_MCP_TOKEN_MAKO=stale-bearer",
		"AGENT_MAKO_SESSION=stale-agent-sess",
		"AGENT_MAKO_PARENT=stale-parent",
		"AGENT_MAKO_PROFILE=stale-profile",
	}
	overrides := map[string]string{
		"AGENT_MAKO_SESSION": "sess-new",
		"AGENT_MAKO_PROJECT": "/work/app",
		"AGENT_MAKO_MODE":    "main",
		"MAKO_LIVE_SOCKET":   "/run/live.sock",
		"MAKO_LIVE_TOKEN":    "fresh-live-tok",
		"NO_PROXY":           "corp.local,127.0.0.1,localhost,::1",
		"no_proxy":           "corp.local,127.0.0.1,localhost,::1",
		"USER_CUSTOM":        "1",
	}

	got := sessionEnv(ambient, "sess-new", "scope-new", overrides)
	byKey := map[string][]string{}
	for _, kv := range got {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("malformed env entry %q", kv)
		}
		byKey[k] = append(byKey[k], v)
	}

	wantExact := map[string]string{
		"HOME":               "/home/me",
		"PATH":               "/usr/bin",
		"TERM":               "xterm-256color",
		"TERM_PROGRAM":       "ghostty",
		"MAKO_SESSION":       "sess-new",
		"MAKO_SCOPE":         "scope-new",
		"AGENT_MAKO_SESSION": "sess-new",
		"AGENT_MAKO_PROJECT": "/work/app",
		"AGENT_MAKO_MODE":    "main",
		"MAKO_LIVE_SOCKET":   "/run/live.sock",
		"MAKO_LIVE_TOKEN":    "fresh-live-tok",
		"NO_PROXY":           "corp.local,127.0.0.1,localhost,::1",
		"no_proxy":           "corp.local,127.0.0.1,localhost,::1",
		"USER_CUSTOM":        "1",
	}
	for k, want := range wantExact {
		if vals := byKey[k]; !slices.Equal(vals, []string{want}) {
			t.Errorf("%s = %q, want [%q]", k, vals, want)
		}
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
		"MAKO_AGENT_MCP_TOKEN",
		"MAKO_MCP_TOKEN_MAKO",
		"AGENT_MAKO_PARENT",
		"AGENT_MAKO_PROFILE",
	} {
		if vals, ok := byKey[gone]; ok {
			t.Errorf("leaked stale env %s=%q", gone, vals)
		}
	}

	// Explicit user override of a session-scoped key still wins.
	explicit := sessionEnv(ambient, "sess-new", "scope-new", map[string]string{
		"CODEX_THREAD_ID": "explicit-thread",
	})
	if !slices.Contains(explicit, "CODEX_THREAD_ID=explicit-thread") {
		t.Fatalf("explicit CODEX_THREAD_ID override lost: %q", explicit)
	}
}
