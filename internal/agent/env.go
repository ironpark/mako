package agent

import (
	"maps"
	"os"
	"runtime"
	"slices"
	"strings"
)

// sessionScopedEnvKeys are environment variables tied to one particular Mako
// or provider agent session. A daemon can outlive the terminal that launched
// it, so inherited values must never leak into new unrelated child sessions or
// one-shot prompts unless explicitly supplied for that launch.
var sessionScopedEnvKeys = map[string]bool{
	// Mako per-session identity, process-tree sweep, live-stream, and IPC tokens.
	"MAKO_SESSION":           true,
	"MAKO_SCOPE":             true,
	"MAKO_LIVE_SOCKET":       true,
	"MAKO_LIVE_TOKEN":        true,
	"MAKO_AGENT_MCP_TOKEN":   true,
	"MAKO_CODEX_WS_TOKEN":    true,
	"AGENT_MAKO_SESSION":     true,
	"AGENT_MAKO_PROJECT":     true,
	"AGENT_MAKO_MODE":        true,
	"AGENT_MAKO_NODE":        true,
	"AGENT_MAKO_PROFILE":     true,
	"AGENT_MAKO_PARENT":      true,
	"AGENT_MAKO_PARENT_NODE": true,
	"AGENT_MAKO_SOCKET":      true,

	// Codex CLI per-session state.
	"CODEX_THREAD_ID": true,

	// Claude Code nested-session guard and per-session runtime state.
	"CLAUDECODE":                    true,
	"CLAUDE_CODE_ENTRYPOINT":        true,
	"CLAUDE_CODE_SESSION_ID":        true,
	"CLAUDE_SESSION_ID":             true,
	"CLAUDE_CODE_PARENT_SESSION_ID": true,
	"CLAUDE_CODE_SSE_PORT":          true,

	// Antigravity (agy) per-session / language-server runtime state.
	"ANTIGRAVITY_AGENT":           true,
	"ANTIGRAVITY_AGENTAPI_EXE":    true,
	"ANTIGRAVITY_APP_DATA_DIR":    true,
	"ANTIGRAVITY_CONVERSATION_ID": true,
	"ANTIGRAVITY_CSRF_TOKEN":      true,
	"ANTIGRAVITY_LS_ADDRESS":      true,
	"ANTIGRAVITY_LS_VERSION":      true,
	"ANTIGRAVITY_PROJECT_ID":      true,
	"ANTIGRAVITY_SOURCE_METADATA": true,
	"ANTIGRAVITY_TRAJECTORY_ID":   true,
}

// makoMCPTokenEnvPrefix is the prefix of per-session Codex HTTP MCP bearer
// token environment variables (see internal/agent/codex).
const makoMCPTokenEnvPrefix = "MAKO_MCP_TOKEN_"

// IsSessionScopedEnv reports whether key is a session-scoped environment
// variable that must be stripped when inheriting ambient daemon environment.
func IsSessionScopedEnv(key string) bool {
	if key == "" {
		return false
	}
	upper := strings.ToUpper(key)
	if sessionScopedEnvKeys[upper] {
		return true
	}
	return strings.HasPrefix(upper, makoMCPTokenEnvPrefix) && len(upper) > len(makoMCPTokenEnvPrefix)
}

// SanitizeEnv returns a copy of environ with all session-scoped variables
// removed. The input slice is not modified.
func SanitizeEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		k, _, ok := cutEnv(kv)
		if !ok || IsSessionScopedEnv(k) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// SanitizeEnvMap returns a clone of env with all session-scoped keys removed.
// The input map is not modified.
func SanitizeEnvMap(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if IsSessionScopedEnv(k) {
			continue
		}
		out[k] = v
	}
	return out
}

// BuildChildEnv constructs a child process environment from a sanitized copy
// of ambient plus explicit per-launch overrides. Explicit overrides always win
// over sanitization and replace any inherited entries for the same variable
// without leaving duplicate keys.
func BuildChildEnv(ambient []string, overrides map[string]string) []string {
	return buildChildEnv(ambient, overrides, runtime.GOOS == "windows")
}

func buildChildEnv(ambient []string, overrides map[string]string, caseInsensitive bool) []string {
	norm := func(k string) string {
		if caseInsensitive {
			return strings.ToUpper(k)
		}
		return k
	}

	overrideKeys := slices.Sorted(maps.Keys(overrides))
	overridden := make(map[string]bool, len(overrideKeys))
	for _, k := range overrideKeys {
		if k == "" {
			continue
		}
		overridden[norm(k)] = true
	}

	out := make([]string, 0, len(ambient)+len(overrideKeys))
	seenIdx := make(map[string]int, len(ambient))
	for _, kv := range ambient {
		k, _, ok := cutEnv(kv)
		if !ok || k == "" || IsSessionScopedEnv(k) {
			continue
		}
		nk := norm(k)
		if overridden[nk] {
			continue
		}
		if idx, exists := seenIdx[nk]; exists {
			out[idx] = kv
			continue
		}
		seenIdx[nk] = len(out)
		out = append(out, kv)
	}

	emittedOverride := make(map[string]bool, len(overrideKeys))
	for _, k := range overrideKeys {
		if k == "" {
			continue
		}
		nk := norm(k)
		if emittedOverride[nk] {
			continue
		}
		emittedOverride[nk] = true
		out = append(out, k+"="+overrides[k])
	}
	return out
}

// SanitizeProcessEnv removes any inherited session-scoped environment variables
// from the current process environment. Daemons call this on startup (after
// HookMain dispatch and login-shell loading) so ambient parent-terminal state
// does not linger for the daemon's lifetime.
func SanitizeProcessEnv() {
	for _, kv := range os.Environ() {
		if k, _, ok := cutEnv(kv); ok && IsSessionScopedEnv(k) {
			_ = os.Unsetenv(k)
		}
	}
}

// cutEnv splits a "KEY=VALUE" environment entry while preserving leading '='
// drive-letter pseudo-variables on Windows (e.g. "=C:=C:\work").
func cutEnv(kv string) (key, val string, ok bool) {
	start := 0
	if strings.HasPrefix(kv, "=") {
		start = 1
	}
	i := strings.IndexByte(kv[start:], '=')
	if i < 0 {
		return kv, "", false
	}
	i += start
	return kv[:i], kv[i+1:], true
}
