package claude

import (
	"encoding/json"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

func TestClaudeHooksAreSettings(t *testing.T) {
	claude := New()
	got, ok := claude.WithHooks([]string{"claude", "--model", "opus"}, "'/bin/makod' hook '/run/h.sock'", "'/bin/makod' statusline '/run/h.sock'")
	if !ok || len(got) != 5 || got[3] != "--settings" {
		t.Fatalf("hook=%q %v", got, ok)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct{ Type, Command string }
		}
		StatusLine struct{ Type, Command string }
	}
	if err := json.Unmarshal([]byte(got[4]), &settings); err != nil {
		t.Fatal(err)
	}
	if h := settings.Hooks["SessionStart"]; len(h) != 1 || len(h[0].Hooks) != 1 || h[0].Hooks[0].Type != "command" || h[0].Hooks[0].Command != "'/bin/makod' hook '/run/h.sock'" {
		t.Fatalf("settings=%s", got[4])
	}
	if sl := settings.StatusLine; sl.Type != "command" || sl.Command != "'/bin/makod' statusline '/run/h.sock'" {
		t.Fatalf("no status line: %s", got[4])
	}
	// Claude's handling of a second --settings is undocumented.
	if _, ok := claude.WithHooks([]string{"claude", "--settings=/mine.json"}, "h", "s"); ok {
		t.Fatal("hook replaced the user's own settings")
	}
	if h := settings.Hooks["PermissionRequest"]; len(h) != 1 || h[0].Hooks[0].Command != "'/bin/makod' hook '/run/h.sock'" {
		t.Fatalf("no approval hook: %s", got[4])
	}
	if ev, ok := claude.Event([]byte(`{"hook_event_name":"SessionStart","session_id":"s","transcript_path":"/p"}`)); !ok || ev.Kind != agent.Started || ev.SessionID != "s" || ev.Transcript != "/p" {
		t.Fatalf("parse=%+v %v", ev, ok)
	}
	if ev, ok := claude.Event([]byte(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`)); !ok || ev.Kind != agent.Approval || ev.Question || ev.Tool != "Bash" || string(ev.Input) != `{"command":"ls"}` {
		t.Fatalf("parse=%+v %v", ev, ok)
	}
	if ev, ok := claude.Event([]byte(`{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion"}`)); !ok || !ev.Question {
		t.Fatalf("a question taken for an approval: %+v", ev)
	}
	if ev, ok := claude.Event([]byte(`{"hook_event_name":"Notification","notification_type":"elicitation_dialog"}`)); !ok || ev.Kind != agent.Waiting {
		t.Fatalf("notification=%+v %v", ev, ok)
	}
	for _, other := range []string{`{"hook_event_name":"SessionStart","session_id":"s","agent_id":"sub"}`, `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, `not json`, `{}`} {
		if _, ok := claude.Event([]byte(other)); ok {
			t.Errorf("%s taken", other)
		}
	}
}
