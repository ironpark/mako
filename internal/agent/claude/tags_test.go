package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestTaggedTurnsReadAsTheUserSawThem(t *testing.T) {
	line := func(fields map[string]any) string {
		b, _ := json.Marshal(fields)
		return string(b) + "\n"
	}
	user := func(uuid string, content any, extra ...string) string {
		r := map[string]any{"type": "user", "uuid": uuid, "message": map[string]any{"role": "user", "content": content}}
		for _, k := range extra {
			r[k] = true
		}
		return line(r)
	}
	body := user("1", "<local-command-caveat>Caveat: run directly.</local-command-caveat>", "isMeta") +
		user("2", "<command-message>simplify</command-message>\n<command-name>/simplify</command-name>") +
		user("3", []map[string]any{{"type": "text", "text": "skill prompt"}}, "isMeta") +
		// /compact as typed, then the summary, then the command again.
		user("4", "/compact keep it short") +
		user("5", "This session is being continued from a previous conversation.\n\nSummary.", "isCompactSummary") +
		user("5a", "<local-command-caveat>Caveat: run directly.</local-command-caveat>", "isMeta") +
		user("5b", "<command-name>/compact</command-name>\n            <command-message>compact</command-message>\n            <command-args>keep it short</command-args>") +
		user("6", "<local-command-stdout>\x1b[2mCompacted (ctrl+o to see full summary)\x1b[22m</local-command-stdout>") +
		user("7", "<task-notification>\n<task-id>a1</task-id>\n<tool-use-id>toolu_1</tool-use-id>\n<status>completed</status>\n<summary>Agent \"Reuse review\" finished</summary>\n<result>Five findings.</result>\n</task-notification>") +
		user("8", "<system-reminder>\nToday's date is 2026-10-27.\n</system-reminder>") +
		user("9", []map[string]any{
			{"type": "tool_result", "tool_use_id": "t1", "content": "ok\n<system-reminder>\n" + midTurn + "also this\n\nThis is how Claude Code surfaces messages.\n</system-reminder>"},
			{"type": "text", "text": "<system-reminder>\nnote\n</system-reminder>\nfix the tests"},
		}) +
		user("10", "look:\n<pasted_content id=\"1\">\n<command-name>/simplify</command-name>\n</pasted_content id=\"1\">")
	messages := decode(body)
	type row struct{ role, tool, detail, text string }
	want := []row{
		{"user", "", "", "/simplify"},
		{"user", "", "", "/compact keep it short"},
		{"tool", "Summary", "This session is being continued from a previous conversation.", "This session is being continued from a previous conversation.\n\nSummary."},
		{"tool", "/compact", "Compacted (ctrl+o to see full summary)", "Compacted (ctrl+o to see full summary)"},
		{"tool", "Task", `Agent "Reuse review" finished`, "Five findings."},
		{"tool", "", "", "ok\n<system-reminder>\n" + midTurn + "also this\n\nThis is how Claude Code surfaces messages.\n</system-reminder>"},
		{"user", "", "", "also this"},
		{"user", "", "", "fix the tests"},
		{"user", "", "", "look:\n\n<command-name>/simplify</command-name>"},
	}
	var got []row
	for _, m := range messages {
		got = append(got, row{m.GetRole(), m.GetTool(), m.GetDetail(), m.GetText()})
	}
	if len(got) != len(want) {
		t.Fatalf("got %d messages, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d: got %q, want %q", i, got[i], want[i])
		}
	}
	if !messages[3].GetOutput() || !messages[4].GetOutput() {
		t.Error("command output and task notice are not output rows")
	}
}

// decode is what Claude's decoder reads of the lines of body.
func decode(body string) []*protocol.ChatMessage {
	dec := New().NewDecoder()
	var out []*protocol.ChatMessage
	for line := range strings.Lines(body) {
		out = append(out, dec.Line([]byte(line))...)
	}
	return out
}
