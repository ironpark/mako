package claude

import (
	"encoding/json"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestToolsReadAsTheirKindsForm(t *testing.T) {
	a := New()
	for _, tc := range []struct {
		name, args, kind, want string
	}{
		{"Bash", `{"command":"go test ./...","description":"Run tests","timeout":1000}`, protocol.ToolShell, `{"command":"go test ./...","description":"Run tests"}`},
		{"Edit", `{"file_path":"/a.go","old_string":"x","new_string":"y"}`, protocol.ToolEdit, `{"new":"y","old":"x","path":"/a.go"}`},
		{"str_replace_based_edit_tool", `{"command":"create","path":"/b.go","file_text":"package b"}`, protocol.ToolWrite, `{"content":"package b","path":"/b.go"}`},
		{"Read", `{"file_path":"/a.go","offset":12}`, protocol.ToolRead, `{"offset":12,"path":"/a.go"}`},
		{"TodoWrite", `{"todos":[{"content":"Fix it","status":"in_progress","activeForm":"Fixing"}]}`, protocol.ToolTodo, `{"items":[{"status":"in_progress","text":"Fix it"}]}`},
		{"Agent", `{"description":"Look","prompt":"Find it.","subagent_type":"Explore"}`, protocol.ToolTask, `{"agent":"Explore","description":"Look","prompt":"Find it."}`},
		{"mcp__x__y", `{"a":1}`, "", `{"a":1}`},
	} {
		kind, read := a.Tool(tc.name, json.RawMessage(tc.args))
		canon, _ := json.Marshal(read)
		if read == nil {
			canon = []byte(tc.args)
		}
		if kind != tc.kind || string(canon) != tc.want {
			t.Errorf("%s: %q %s, want %q %s", tc.name, kind, canon, tc.kind, tc.want)
		}
	}
}

func TestReadOutputLosesItsLineNumbersAndNotes(t *testing.T) {
	a := New()
	if text, _ := a.Output(protocol.ToolRead, "    12→package gui\n    13→\n    14→func x() {}\n\n<system-reminder>\nnote\n</system-reminder>\n"); text != "package gui\n\nfunc x() {}" {
		t.Fatalf("text %q", text)
	}
	if text, _ := a.Output(protocol.ToolRead, "no numbers here"); text != "no numbers here" {
		t.Fatalf("plain text changed: %q", text)
	}
}
