package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

func TestClaudeHooksAreSettings(t *testing.T) {
	claude := New()
	l := agent.Launch{Command: []string{"claude", "--model", "opus"}}
	ok := claude.WithHooks(&l, "'/bin/makod' hook '/run/h.sock'", "'/bin/makod' statusline '/run/h.sock'", "")
	got := l.Command
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
	if claude.WithHooks(&agent.Launch{Command: []string{"claude", "--settings=/mine.json"}}, "h", "s", "") {
		t.Fatal("hook replaced the user's own settings")
	}
	if h := settings.Hooks["PermissionRequest"]; len(h) != 1 || h[0].Hooks[0].Command != "'/bin/makod' hook '/run/h.sock'" {
		t.Fatalf("no approval hook: %s", got[4])
	}
	if ev, ok := claude.Event([]byte(`{"hook_event_name":"SessionStart","session_id":"s","transcript_path":"/p"}`)); !ok || ev.Kind != agent.Started || ev.SessionID != "s" || ev.Transcript != "/p" {
		t.Fatalf("parse=%+v %v", ev, ok)
	}
	if ev, ok := claude.Event([]byte(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`)); !ok || ev.Kind != agent.Approval || ev.Tool != "Bash" || string(ev.Input) != `{"command":"ls"}` {
		t.Fatalf("parse=%+v %v", ev, ok)
	}
	if kind, _ := claude.Tool("AskUserQuestion", nil); kind != protocol.ToolQuestion {
		t.Fatalf("AskUserQuestion is a %q", kind)
	}
	if ev, ok := claude.Event([]byte(`{"hook_event_name":"Notification","notification_type":"elicitation_dialog"}`)); !ok || ev.Kind != agent.Waiting {
		t.Fatalf("notification=%+v %v", ev, ok)
	}
	for _, step := range []struct {
		input string
		kind  agent.EventKind
	}{
		{`{"hook_event_name":"SubagentStart","session_id":"s","agent_id":"sub"}`, agent.ChildStarted},
		{`{"hook_event_name":"SubagentStop","session_id":"s","agent_id":"sub"}`, agent.ChildStopped},
		{`{"hook_event_name":"StopFailure","session_id":"s"}`, agent.Idle},
		{`{"hook_event_name":"Notification","notification_type":"idle_prompt","session_id":"s"}`, agent.Idle},
	} {
		ev, ok := claude.Event([]byte(step.input))
		if !ok || ev.Kind != step.kind || ev.SessionID != "s" {
			t.Fatalf("%s: %+v, %v", step.input, ev, ok)
		}
		if (step.kind == agent.ChildStarted || step.kind == agent.ChildStopped) && ev.AgentID != "sub" {
			t.Fatal("child identity lost")
		}
	}
	for _, event := range []string{"SubagentStart", "SubagentStop", "StopFailure"} {
		if len(settings.Hooks[event]) == 0 {
			t.Fatalf("%s is not installed", event)
		}
	}
	for _, other := range []string{`{"hook_event_name":"SessionStart","session_id":"s","agent_id":"sub"}`, `not json`, `{}`} {
		if _, ok := claude.Event([]byte(other)); ok {
			t.Errorf("%s taken", other)
		}
	}
}

func TestClaudeWithHooksMergesExistingSettings(t *testing.T) {
	c := New()
	// Inline JSON --settings merges Mako hooks alongside user hooks and sets statusLine.
	l1 := agent.Launch{Command: []string{"claude", "--settings", `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo user"}]}]}}`}}
	if !c.WithHooks(&l1, "/bin/makod hook /s", "/bin/makod statusline /s", "") {
		t.Fatal("WithHooks returned false for valid inline --settings")
	}
	var merged map[string]any
	if err := json.Unmarshal([]byte(l1.Command[2]), &merged); err != nil {
		t.Fatalf("invalid merged JSON: %v", err)
	}
	if merged["model"] != "opus" {
		t.Fatalf("lost user setting: %+v", merged)
	}
	hooksMap, _ := merged["hooks"].(map[string]any)
	stopList, _ := hooksMap["Stop"].([]any)
	if len(stopList) != 2 {
		t.Fatalf("expected user Stop hook preserved alongside Mako's, got %+v", stopList)
	}
	if merged["statusLine"] == nil {
		t.Fatalf("expected statusLine added: %+v", merged)
	}

	// File path --settings with explicit user statusLine preserves user statusLine.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "custom.json")
	if err := os.WriteFile(cfgPath, []byte(`{"statusLine":{"type":"command","command":"my-status"},"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	l2 := agent.Launch{Command: []string{"claude", "--settings=" + cfgPath}}
	if !c.WithHooks(&l2, "/bin/makod hook /s", "/bin/makod statusline /s", "") {
		t.Fatal("WithHooks returned false for readable --settings file")
	}
	raw := strings.TrimPrefix(l2.Command[1], "--settings=")
	var merged2 map[string]any
	if err := json.Unmarshal([]byte(raw), &merged2); err != nil {
		t.Fatalf("invalid merged file JSON: %v", err)
	}
	sl, _ := merged2["statusLine"].(map[string]any)
	if sl["command"] != "my-status" || merged2["theme"] != "dark" || merged2["hooks"] == nil {
		t.Fatalf("unexpected merged2: %+v", merged2)
	}
}

func TestClaudeWithCommitSignature(t *testing.T) {
	c := New()

	// Enabled leaves command untouched.
	lOn := agent.Launch{Command: []string{"claude", "--model", "opus"}}
	c.WithCommitSignature(&lOn, true)
	if len(lOn.Command) != 3 {
		t.Fatalf("enabled modified command: %q", lOn.Command)
	}

	// Disabled adds --settings with attribution.commit = "".
	lOff := agent.Launch{Command: []string{"claude", "--model", "opus"}}
	c.WithCommitSignature(&lOff, false)
	if len(lOff.Command) != 5 || lOff.Command[3] != "--settings" || lOff.Command[4] != `{"attribution":{"commit":""}}` {
		t.Fatalf("disabled command = %q", lOff.Command)
	}

	// WithHooks merges cleanly into the --settings added by WithCommitSignature.
	if !c.WithHooks(&lOff, "/bin/makod hook /s", "/bin/makod statusline /s", "") {
		t.Fatal("WithHooks failed after WithCommitSignature")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(lOff.Command[4]), &doc); err != nil {
		t.Fatalf("invalid merged JSON: %v", err)
	}
	attr, _ := doc["attribution"].(map[string]any)
	if v, ok := attr["commit"].(string); !ok || v != "" {
		t.Fatalf("attribution.commit lost after WithHooks: %+v", doc)
	}
	if doc["hooks"] == nil || doc["statusLine"] == nil {
		t.Fatalf("hooks/statusLine missing: %+v", doc)
	}

	// Preserves existing attribution.pr and other settings when merging.
	lExisting := agent.Launch{Command: []string{"claude", "--settings", `{"theme":"dark","attribution":{"pr":"custom"}}`}}
	c.WithCommitSignature(&lExisting, false)
	var doc2 map[string]any
	if err := json.Unmarshal([]byte(lExisting.Command[2]), &doc2); err != nil {
		t.Fatal(err)
	}
	attr2, _ := doc2["attribution"].(map[string]any)
	if doc2["theme"] != "dark" || attr2["pr"] != "custom" || attr2["commit"] != "" {
		t.Fatalf("unexpected merged attribution: %+v", doc2)
	}

	// Preserves attribution: false without overwriting it.
	lFalse := agent.Launch{Command: []string{"claude", "--settings", `{"attribution":false}`}}
	c.WithCommitSignature(&lFalse, false)
	var doc3 map[string]any
	if err := json.Unmarshal([]byte(lFalse.Command[2]), &doc3); err != nil {
		t.Fatal(err)
	}
	if b, ok := doc3["attribution"].(bool); !ok || b {
		t.Fatalf("expected attribution=false preserved, got %+v", doc3)
	}
}
