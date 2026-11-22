package gui

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestPatchesSplitByFile(t *testing.T) {
	files := parsePatch("*** Begin Patch\n*** Update File: a.go\n@@ func a\n-old\n+new\n ctx\n*** Add File: b.go\n+package b\n*** Delete File: c.go\n*** End Patch\n")
	if len(files) != 3 || files[0].Path != "a.go" || files[1].Op != "add" || files[2].Op != "delete" {
		t.Fatalf("files %+v", files)
	}
	ops := ""
	for _, l := range files[0].Lines {
		ops += l.GetOp()
	}
	if ops != "@-+ " || files[0].Lines[2].GetText() != "new" {
		t.Fatalf("lines %v", files[0].Lines)
	}
}

func TestSwarmToolSummariesAndClassification(t *testing.T) {
	cases := []struct {
		name      string
		msg       *protocol.ChatMessage
		wantLabel string
		wantDet   string
		wantIcon  string
		wantSync  bool
	}{
		{
			name:      "read_board_since",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "read_board", Text: `{"since":68,"limit":20}`},
			wantLabel: T("Read board"),
			wantDet:   Tf("since #%d", 68),
			wantIcon:  "messages-square",
			wantSync:  true,
		},
		{
			name:      "read_board_status",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "read_board", Text: `{"status":true}`},
			wantLabel: T("Read board"),
			wantDet:   T("roster status"),
			wantIcon:  "messages-square",
			wantSync:  true,
		},
		{
			name:      "read_board_seqs",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "read_board", Text: `{"seqs":[4,70]}`},
			wantLabel: T("Read board"),
			wantDet:   "#4, #70",
			wantIcon:  "messages-square",
			wantSync:  true,
		},
		{
			name:      "swarm_wait_mentions",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "swarm_wait", Text: `{"since":70}`},
			wantLabel: T("Wait for board"),
			wantDet:   Tf("since #%d · mentions", 70),
			wantIcon:  "clock",
			wantSync:  true,
		},
		{
			name:      "swarm_wait_all",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "swarm_wait", Text: `{"since":70,"mentions_only":false}`},
			wantLabel: T("Wait for board"),
			wantDet:   Tf("since #%d · all messages", 70),
			wantIcon:  "clock",
			wantSync:  true,
		},
		{
			name:      "post_message",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "post_message", Text: `{"text":"@coordinator [done] Fixed tests\nDetails..."}`},
			wantLabel: T("Post to board"),
			wantDet:   "@coordinator [done] Fixed tests",
			wantIcon:  "send-horizontal",
			wantSync:  false,
		},
		{
			name:      "swarm_add",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "swarm", Text: `{"action":"add","name":"ui-schema","brief":"Update proto"}`},
			wantLabel: T("Swarm"),
			wantDet:   "add ui-schema · Update proto",
			wantIcon:  "users",
			wantSync:  false,
		},
		{
			name:      "swarm_status",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "swarm", Text: `{"action":"status"}`},
			wantLabel: T("Swarm"),
			wantDet:   T("status"),
			wantIcon:  "users",
			wantSync:  true,
		},
		{
			name:      "manage_task_list",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "manage_task", Text: `{"Action":"list"}`},
			wantLabel: T("Manage task"),
			wantDet:   T("list tasks"),
			wantIcon:  "list",
			wantSync:  true,
		},
		{
			name:      "manage_task_kill",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "manage_task", Text: `{"Action":"kill","TaskId":"cron-1"}`},
			wantLabel: T("Manage task"),
			wantDet:   "kill cron-1",
			wantIcon:  "list",
			wantSync:  true,
		},
		{
			name:      "schedule_cron",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "schedule", Text: `{"CronExpression":"*/3 * * * *","Prompt":"Check swarm progress"}`},
			wantLabel: T("Schedule"),
			wantDet:   "*/3 * * * * · Check swarm progress",
			wantIcon:  "clock",
			wantSync:  true,
		},
		{
			name:      "schedule_timer",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "schedule", Text: `{"DurationSeconds":180,"Prompt":"Check stall"}`},
			wantLabel: T("Schedule"),
			wantDet:   "3m · Check stall",
			wantIcon:  "clock",
			wantSync:  true,
		},
		{
			name:      "invoke_subagent",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "invoke_subagent", Kind: protocol.ToolTask, Text: `{"agent":"research","description":"Code Explorer","prompt":"Inspect MCP"}`},
			wantLabel: T("Subagent"),
			wantDet:   "research · Code Explorer",
			wantIcon:  "users",
			wantSync:  false,
		},
		{
			name:      "send_message",
			msg:       &protocol.ChatMessage{Role: "tool", Tool: "send_message", Text: `{"to":"ab12","text":"Build passed\nAll green"}`},
			wantLabel: T("Send message"),
			wantDet:   "→ ab12: Build passed",
			wantIcon:  "send-horizontal",
			wantSync:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, det := toolDisplay(tc.msg)
			if label != tc.wantLabel || det != tc.wantDet {
				t.Fatalf("toolDisplay() = (%q, %q), want (%q, %q)", label, det, tc.wantLabel, tc.wantDet)
			}
			if got := toolRowGlyph(tc.msg); got != tc.wantIcon {
				t.Fatalf("toolRowGlyph() = %q, want %q", got, tc.wantIcon)
			}
			if got := isSwarmSyncTool(tc.msg); got != tc.wantSync {
				t.Fatalf("isSwarmSyncTool() = %v, want %v", got, tc.wantSync)
			}
		})
	}
}
