package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"unicode"

	"github.com/ironpark/mako/internal/agent"
)

// Codex takes hooks as -c overrides of its hooks table, which it trusts
// only by each one's hash, and only in versions with the hooks feature.

// hookEvents are the events mako hooks. Codex has no Notification: it asks
// the user only for approvals.
var hookEvents = []agent.HookEvent{
	{Name: "SessionStart", Timeout: agent.HookTimeout}, {Name: "UserPromptSubmit", Timeout: agent.HookTimeout},
	{Name: "PostToolUse", Timeout: agent.HookTimeout}, {Name: "PermissionRequest", Timeout: agent.ApprovalTimeout},
	{Name: "PreCompact", Timeout: agent.HookTimeout}, {Name: "Stop", Timeout: agent.HookTimeout},
	{Name: "SessionEnd", Timeout: 3},
}

// HookFeature is the listing that says whether this version has hooks.
func (*Agent) HookFeature() ([]string, string) { return []string{"features", "list"}, "hooks" }

// WithHooks gives the hooks as overrides, before any subcommand, as the
// program's own options, with the trust each needs. The status line is
// Claude's alone.
func (*Agent) WithHooks(l *agent.Launch, hook, _, _ string) bool {
	args := []string{l.Command[0]}
	var trust []string
	for _, e := range hookEvents {
		args = append(args, "-c", fmt.Sprintf("hooks.%s=[{hooks=[{type=%s,command=%s,timeout=%d}]}]",
			e.Name, tomlString("command"), tomlString(hook), e.Timeout))
		key := fmt.Sprintf("%s:%s:0:0", sessionFlagsSource(), eventKey(e.Name))
		trust = append(trust, fmt.Sprintf("%s={trusted_hash=%s}", tomlString(key), tomlString(hookHash(e, hook))))
	}
	// The hooks come from this command line, which Codex trusts only by
	// each one's hash, as /hooks would record it.
	args = append(args, "-c", "hooks.state={"+strings.Join(trust, ",")+"}")
	l.Command = append(args, l.Command[1:]...)
	return true
}

// sessionFlagsSource is where Codex says hooks from -c come from, as the
// keys of their trust name it.
func sessionFlagsSource() string {
	if runtime.GOOS == "windows" {
		return `C:\<session-flags>\config.toml`
	}
	return "/<session-flags>/config.toml"
}

// eventKey is Codex's name for an event in a hook's key: snake case.
func eventKey(name string) string {
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// hookHash is the hash Codex trusts a hook by: of its event and
// handler, normalized, as JSON with sorted keys.
func hookHash(e agent.HookEvent, command string) string {
	identity := map[string]any{
		"event_name": eventKey(e.Name),
		"hooks": []map[string]any{{
			"type": "command", "command": command, "timeout": e.Timeout, "async": false,
		}},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(identity); err != nil {
		panic(err) // plain values
	}
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
