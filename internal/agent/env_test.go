package agent

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestIsSessionScopedEnv(t *testing.T) {
	shouldStrip := []string{
		"MAKO_SESSION",
		"MAKO_SCOPE",
		"MAKO_LIVE_SOCKET",
		"MAKO_LIVE_TOKEN",
		"MAKO_AGENT_MCP_TOKEN",
		"MAKO_CODEX_WS_TOKEN",
		"MAKO_MCP_TOKEN_MAKO",
		"MAKO_MCP_TOKEN_CUSTOM_1",
		"AGENT_MAKO_SESSION",
		"AGENT_MAKO_PROJECT",
		"AGENT_MAKO_MODE",
		"AGENT_MAKO_NODE",
		"AGENT_MAKO_PROFILE",
		"AGENT_MAKO_PARENT",
		"AGENT_MAKO_PARENT_NODE",
		"AGENT_MAKO_SOCKET",
		"CODEX_THREAD_ID",
		"codex_thread_id",
		"CLAUDECODE",
		"CLAUDE_CODE_ENTRYPOINT",
		"CLAUDE_CODE_SESSION_ID",
		"CLAUDE_SESSION_ID",
		"CLAUDE_CODE_PARENT_SESSION_ID",
		"CLAUDE_CODE_SSE_PORT",
		"ANTIGRAVITY_AGENT",
		"ANTIGRAVITY_AGENTAPI_EXE",
		"ANTIGRAVITY_APP_DATA_DIR",
		"ANTIGRAVITY_CONVERSATION_ID",
		"ANTIGRAVITY_CSRF_TOKEN",
		"ANTIGRAVITY_LS_ADDRESS",
		"ANTIGRAVITY_LS_VERSION",
		"ANTIGRAVITY_PROJECT_ID",
		"ANTIGRAVITY_SOURCE_METADATA",
		"ANTIGRAVITY_TRAJECTORY_ID",
	}
	for _, k := range shouldStrip {
		if !IsSessionScopedEnv(k) {
			t.Errorf("IsSessionScopedEnv(%q) = false, want true", k)
		}
	}

	mustKeep := []string{
		"",
		"HOME",
		"PATH",
		"MAKO_HOME",
		"MAKO_KEYCHAIN",
		"MAKO_PPROF",
		"MAKO_PLUGINS",
		"AGENT_MAKO_PLUGINS",
		"MAKO_MCP_TOKEN_",
		"CODEX_HOME",
		"CLAUDE_CONFIG_DIR",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_USE_VERTEX",
		"CLAUDE_CODE_GIT_BASH_PATH",
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"GEMINI_API_KEY",
		"ANTIGRAVITY_HOME",
		"OPENCODE_CONFIG_CONTENT",
		"PI_CODING_AGENT_DIR",
		"NO_PROXY",
		"no_proxy",
		"HTTP_PROXY",
		"HTTPS_PROXY",
	}
	for _, k := range mustKeep {
		if IsSessionScopedEnv(k) {
			t.Errorf("IsSessionScopedEnv(%q) = true, want false", k)
		}
	}
}

func TestSanitizeEnvDoesNotMutateSource(t *testing.T) {
	source := []string{
		"HOME=/home/user",
		"CODEX_THREAD_ID=stale-thread",
		"CLAUDECODE=1",
		"MAKO_SESSION=stale-sess",
		"MAKO_MCP_TOKEN_MAKO=stale-secret",
		"ANTIGRAVITY_CONVERSATION_ID=stale-conv",
		"CLAUDE_CONFIG_DIR=/custom/claude",
		"CODEX_HOME=/custom/codex",
	}
	orig := slices.Clone(source)
	got := SanitizeEnv(source)

	if !slices.Equal(source, orig) {
		t.Fatalf("SanitizeEnv mutated source: %q", source)
	}
	want := []string{
		"HOME=/home/user",
		"CLAUDE_CONFIG_DIR=/custom/claude",
		"CODEX_HOME=/custom/codex",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("SanitizeEnv = %q, want %q", got, want)
	}
}

func TestSanitizeEnvMap(t *testing.T) {
	if SanitizeEnvMap(nil) != nil {
		t.Fatal("SanitizeEnvMap(nil) should return nil")
	}
	src := map[string]string{
		"KEEP":            "yes",
		"CODEX_THREAD_ID": "stale",
		"CLAUDECODE":      "1",
		"MAKO_SESSION":    "old",
	}
	got := SanitizeEnvMap(src)
	if got["KEEP"] != "yes" || len(got) != 1 {
		t.Fatalf("SanitizeEnvMap = %v, want only KEEP=yes", got)
	}
	if src["CODEX_THREAD_ID"] != "stale" {
		t.Fatal("SanitizeEnvMap mutated source map")
	}
}

func TestBuildChildEnvSanitizesAmbientAndPreservesExplicitOverrides(t *testing.T) {
	ambient := []string{
		"HOME=/home/user",
		"NO_PROXY=corp.local",
		"no_proxy=corp.local",
		"CODEX_THREAD_ID=stale-thread",
		"CLAUDECODE=1",
		"CLAUDE_CODE_SESSION_ID=stale-claude",
		"ANTIGRAVITY_CONVERSATION_ID=stale-agy",
		"MAKO_SESSION=stale-session",
		"MAKO_SCOPE=stale-scope",
		"MAKO_LIVE_SOCKET=/tmp/old.sock",
		"MAKO_LIVE_TOKEN=stale-live-tok",
		"MAKO_AGENT_MCP_TOKEN=stale-mcp-tok",
		"MAKO_MCP_TOKEN_MAKO=stale-bearer",
		"AGENT_MAKO_SESSION=stale-agent-sess",
		"AGENT_MAKO_PARENT=stale-parent",
		"AGENT_MAKO_PROFILE=stale-profile",
	}
	overrides := map[string]string{
		"MAKO_SESSION":       "new-session",
		"AGENT_MAKO_SESSION": "new-session",
		"AGENT_MAKO_PROJECT": "/work/proj",
		"AGENT_MAKO_MODE":    "main",
		"MAKO_LIVE_SOCKET":   "/tmp/new.sock",
		"MAKO_LIVE_TOKEN":    "new-live-tok",
		"NO_PROXY":           "corp.local,127.0.0.1,localhost,::1",
		"no_proxy":           "corp.local,127.0.0.1,localhost,::1",
		"USER_EXPLICIT_VAR":  "ok",
		"CODEX_THREAD_ID":    "explicit-override-thread",
	}

	got := buildChildEnv(ambient, overrides, false)
	envMap := map[string][]string{}
	for _, kv := range got {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("malformed env entry %q", kv)
		}
		envMap[k] = append(envMap[k], v)
	}

	// Explicit overrides win and appear exactly once.
	for k, want := range overrides {
		if vals := envMap[k]; !slices.Equal(vals, []string{want}) {
			t.Errorf("env[%q] = %q, want [%q]", k, vals, want)
		}
	}
	// Non-session ambient vars survive.
	if vals := envMap["HOME"]; !slices.Equal(vals, []string{"/home/user"}) {
		t.Errorf("HOME = %q, want [/home/user]", vals)
	}
	// Stale session-scoped ambient vars not explicitly overridden are stripped.
	for _, gone := range []string{
		"CLAUDECODE",
		"CLAUDE_CODE_SESSION_ID",
		"ANTIGRAVITY_CONVERSATION_ID",
		"MAKO_SCOPE",
		"MAKO_AGENT_MCP_TOKEN",
		"MAKO_MCP_TOKEN_MAKO",
		"AGENT_MAKO_PARENT",
		"AGENT_MAKO_PROFILE",
	} {
		if vals, exists := envMap[gone]; exists {
			t.Errorf("stale session env %q leaked: %q", gone, vals)
		}
	}
}

func TestBuildChildEnvCaseInsensitiveWindowsDedup(t *testing.T) {
	ambient := []string{
		"=C:=C:\\work",
		"Path=C:\\Windows",
		"no_proxy=old.corp",
		"Codex_Thread_Id=stale",
	}
	overrides := map[string]string{
		"PATH":     "C:\\mako;C:\\Windows",
		"NO_PROXY": "old.corp,127.0.0.1,localhost,::1",
		"no_proxy": "old.corp,127.0.0.1,localhost,::1",
	}

	got := buildChildEnv(ambient, overrides, true)
	if !slices.Contains(got, "=C:=C:\\work") {
		t.Fatalf("lost Windows drive pseudo-var: %q", got)
	}
	if !slices.Contains(got, "PATH=C:\\mako;C:\\Windows") {
		t.Fatalf("missing overridden PATH: %q", got)
	}
	if slices.Contains(got, "Path=C:\\Windows") {
		t.Fatalf("ambient Path should be replaced case-insensitively: %q", got)
	}
	noProxyCount := 0
	for _, kv := range got {
		k, _, _ := cutEnv(kv)
		if strings.EqualFold(k, "CODEX_THREAD_ID") {
			t.Fatalf("Codex_Thread_Id leaked on Windows: %q", got)
		}
		if strings.EqualFold(k, "NO_PROXY") {
			noProxyCount++
		}
	}
	if noProxyCount != 1 {
		t.Fatalf("Windows env should deduplicate NO_PROXY/no_proxy to 1 entry, got %d in %q", noProxyCount, got)
	}
}

func TestSanitizeProcessEnv(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "stale-thread-id")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("MAKO_SESSION", "stale-sess")
	t.Setenv("MAKO_MCP_TOKEN_TEST", "stale-token")
	t.Setenv("MAKO_TEST_KEEP_VAR", "keep-me")

	SanitizeProcessEnv()

	for _, gone := range []string{"CODEX_THREAD_ID", "CLAUDECODE", "MAKO_SESSION", "MAKO_MCP_TOKEN_TEST"} {
		if v, ok := os.LookupEnv(gone); ok {
			t.Errorf("SanitizeProcessEnv left %s=%q", gone, v)
		}
	}
	if got := os.Getenv("MAKO_TEST_KEEP_VAR"); got != "keep-me" {
		t.Errorf("SanitizeProcessEnv removed non-session var: got %q", got)
	}
}
