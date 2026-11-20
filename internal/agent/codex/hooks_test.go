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
		"SubagentStart":     "sha256:986f7ca1bd74d6b334078b8f5af0c23847247b685b4ac6d16383b2a36f375c0c",
		"SubagentStop":      "sha256:671d32187bff2da57e4bda712b5bf182c177d39f15872450dfd316b618fea55f",
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
		`hooks.PostCompact=[{hooks=[{type="command",command="'/bin/san\"bo' hook '/run/h.sock'",timeout=10}]}]`,
		`hooks.Interrupt=[{hooks=[{type="command",command="'/bin/san\"bo' hook '/run/h.sock'",timeout=10}]}]`,
		`hooks.SubagentStart=[{hooks=[{type="command",command="'/bin/san\"bo' hook '/run/h.sock'",timeout=10}]}]`,
		`hooks.SubagentStop=[{hooks=[{type="command",command="'/bin/san\"bo' hook '/run/h.sock'",timeout=10}]}]`,
		`"/<session-flags>/config.toml:permission_request:0:0"={trusted_hash="sha256:`,
		`"/<session-flags>/config.toml:post_compact:0:0"={trusted_hash="sha256:`,
		`"/<session-flags>/config.toml:interrupt:0:0"={trusted_hash="sha256:`,
		`"/<session-flags>/config.toml:subagent_start:0:0"={trusted_hash="sha256:`,
		`"/<session-flags>/config.toml:subagent_stop:0:0"={trusted_hash="sha256:`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %s in %q", want, got)
		}
	}
	if ev, ok := codex.Event([]byte(`{"hook_event_name":"Stop","session_id":"s","transcript_path":null}`)); !ok || ev.Kind != agent.Stopped {
		t.Fatalf("parse=%+v %v", ev, ok)
	}
}

func TestCodexHookEvents(t *testing.T) {
	c := New()

	for _, tc := range []struct {
		name       string
		input      string
		kind       agent.EventKind
		sessionID  string
		agentID    string
		transcript string
	}{
		{
			name:       "SessionStart",
			input:      `{"hook_event_name":"SessionStart","session_id":"s1","transcript_path":"/tmp/rollout.jsonl","source":"startup"}`,
			kind:       agent.Started,
			sessionID:  "s1",
			transcript: "/tmp/rollout.jsonl",
		},
		{
			name:      "UserPromptSubmit",
			input:     `{"hook_event_name":"UserPromptSubmit","session_id":"s1","transcript_path":null}`,
			kind:      agent.Prompted,
			sessionID: "s1",
		},
		{
			name:      "PostToolUse",
			input:     `{"hook_event_name":"PostToolUse","session_id":"s1","tool_name":"Bash"}`,
			kind:      agent.ToolDone,
			sessionID: "s1",
		},
		{
			name:      "PermissionRequest",
			input:     `{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"}}`,
			kind:      agent.Approval,
			sessionID: "s1",
		},
		{
			name:      "PreCompact",
			input:     `{"hook_event_name":"PreCompact","session_id":"s1","trigger":"auto","transcript_path":null}`,
			kind:      agent.Compacting,
			sessionID: "s1",
		},
		{
			name:       "PostCompact auto resumes turn",
			input:      `{"hook_event_name":"PostCompact","session_id":"s1","trigger":"auto","transcript_path":"/tmp/rollout.jsonl"}`,
			kind:       agent.ToolDone,
			sessionID:  "s1",
			transcript: "/tmp/rollout.jsonl",
		},
		{
			name:       "PostCompact manual returns to idle",
			input:      `{"hook_event_name":"PostCompact","session_id":"s1","trigger":"manual","transcript_path":"/tmp/rollout.jsonl"}`,
			kind:       agent.Idle,
			sessionID:  "s1",
			transcript: "/tmp/rollout.jsonl",
		},
		{
			name:      "PostCompact without trigger defaults to resuming turn",
			input:     `{"hook_event_name":"PostCompact","session_id":"s1","transcript_path":null}`,
			kind:      agent.ToolDone,
			sessionID: "s1",
		},
		{
			name:       "Interrupt returns to idle",
			input:      `{"hook_event_name":"Interrupt","session_id":"s1","transcript_path":"/tmp/rollout.jsonl","turn_id":"t1"}`,
			kind:       agent.Idle,
			sessionID:  "s1",
			transcript: "/tmp/rollout.jsonl",
		},
		{
			name:      "Interrupt with null transcript",
			input:     `{"hook_event_name":"Interrupt","session_id":"s1","transcript_path":null,"turn_id":"t1"}`,
			kind:      agent.Idle,
			sessionID: "s1",
		},
		{
			name:      "SubagentStart tracks child",
			input:     `{"hook_event_name":"SubagentStart","session_id":"s1","agent_id":"sub-42","agent_type":"explorer","transcript_path":null,"turn_id":"t1"}`,
			kind:      agent.ChildStarted,
			sessionID: "s1",
			agentID:   "sub-42",
		},
		{
			name:      "SubagentStop clears child",
			input:     `{"hook_event_name":"SubagentStop","session_id":"s1","agent_id":"sub-42","agent_type":"explorer","agent_transcript_path":"/tmp/sub.jsonl","stop_hook_active":false,"transcript_path":null,"turn_id":"t1"}`,
			kind:      agent.ChildStopped,
			sessionID: "s1",
			agentID:   "sub-42",
		},
		{
			name:      "SessionEnd",
			input:     `{"hook_event_name":"SessionEnd","session_id":"s1","reason":"other","transcript_path":null}`,
			kind:      agent.Ended,
			sessionID: "s1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := c.Event([]byte(tc.input))
			if !ok {
				t.Fatalf("Event returned ok=false for %s", tc.input)
			}
			if ev.Kind != tc.kind || ev.SessionID != tc.sessionID || ev.AgentID != tc.agentID || ev.Transcript != tc.transcript {
				t.Fatalf("got %+v, want kind=%v session=%q agent=%q transcript=%q", ev, tc.kind, tc.sessionID, tc.agentID, tc.transcript)
			}
		})
	}

	// Subagent's own non-lifecycle events, missing agent_id on SubagentStart/Stop,
	// unknown events, and malformed payloads must be ignored.
	for _, ignored := range []string{
		`{"hook_event_name":"SessionStart","session_id":"s1","agent_id":"sub-42"}`,
		`{"hook_event_name":"Stop","session_id":"s1","agent_id":"sub-42"}`,
		`{"hook_event_name":"PreCompact","session_id":"s1","agent_id":"sub-42","trigger":"auto"}`,
		`{"hook_event_name":"PostCompact","session_id":"s1","agent_id":"sub-42","trigger":"auto"}`,
		`{"hook_event_name":"Interrupt","session_id":"s1","agent_id":"sub-42"}`,
		`{"hook_event_name":"SubagentStart","session_id":"s1"}`,
		`{"hook_event_name":"SubagentStop","session_id":"s1"}`,
		`{"hook_event_name":"UnknownEvent","session_id":"s1"}`,
		`{}`,
		`not json`,
	} {
		if ev, ok := c.Event([]byte(ignored)); ok {
			t.Errorf("expected ignored for %s, got %+v", ignored, ev)
		}
	}
}
