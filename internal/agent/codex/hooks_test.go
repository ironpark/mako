package codex

import (
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

// Codex takes its hooks as config overrides, which it runs only when
// trusted by the hash it computes of each; these are hashes Codex itself
// recorded on trusting the same hooks.
func TestCodexHooksAreTrustedOverrides(t *testing.T) {
	cmd := "if [ -f '/Users/ironpark/.orca/agent-hooks/codex-hook.sh' ] && [ -r '/Users/ironpark/.orca/agent-hooks/codex-hook.sh' ] && [ -x '/Users/ironpark/.orca/agent-hooks/codex-hook.sh' ]; then /bin/sh '/Users/ironpark/.orca/agent-hooks/codex-hook.sh'; else { command -p cat 2>/dev/null || cat; } >/dev/null 2>&1 || :; fi"
	for name, want := range map[string]string{
		"SessionStart":      "sha256:9a0e1518079e2e24e2cf7d47a4deecd885a9089d5eed8522d16ca7334925b4b9",
		"Stop":              "sha256:062e403078366fd27b0501522dcdd66a9ce9488df3c009329e1de067bfbbfa14",
		"PermissionRequest": "sha256:bf384f5375f7e816fb8d77e977f5b09a37135ae9c136b8908693762f102fe69e",
	} {
		if got := hookHash(agent.HookEvent{Name: name, Timeout: 10}, cmd); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}

	codex := New()
	l := agent.Launch{Command: []string{"codex", "resume", "abc"}}
	ok := codex.WithHooks(&l, `'/bin/san"bo' hook '/run/h.sock'`, "", "")
	got := l.Command
	if !ok || got[0] != "codex" || !slices.Equal(got[len(got)-2:], []string{"resume", "abc"}) {
		t.Fatalf("hook=%q %v", got, ok)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		`hooks.Stop=[{hooks=[{type="command",command="'/bin/san\"bo' hook '/run/h.sock'",timeout=10}]}]`,
		`hooks.PermissionRequest=[{hooks=[{type="command",command="'/bin/san\"bo' hook '/run/h.sock'",timeout=86400}]}]`,
		`"/<session-flags>/config.toml:permission_request:0:0"={trusted_hash="sha256:`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %s in %q", want, got)
		}
	}
	if ev, ok := codex.Event([]byte(`{"hook_event_name":"Stop","session_id":"s","transcript_path":null}`)); !ok || ev.Kind != agent.Stopped {
		t.Fatalf("parse=%+v %v", ev, ok)
	}
}
