package codex

import (
	"encoding/json"
	"testing"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// Codex's shell tools take a command as a string or an argv; each reads as
// the one command the shell kind has, and its output without the header.
func TestShellToolsReadAsOneForm(t *testing.T) {
	a := New()
	for _, tc := range []struct {
		name, args, want string
	}{
		{"exec_command", `{"cmd":"ls -la","workdir":"/p"}`, `{"command":"ls -la","cwd":"/p"}`},
		{"shell", `{"command":["bash","-lc","git status"]}`, `{"command":"git status"}`},
		{"shell", `{"command":["rg","-n","x"]}`, `{"command":"rg -n x"}`},
	} {
		kind, read := a.Tool(tc.name, json.RawMessage(tc.args))
		canon, _ := json.Marshal(read)
		if read == nil {
			canon = []byte(tc.args)
		}
		if kind != protocol.ToolShell || string(canon) != tc.want {
			t.Errorf("%s %s: %s %s, want %s", tc.name, tc.args, kind, canon, tc.want)
		}
	}
	if out, code := a.Output(protocol.ToolShell, "Chunk ID: 1\nWall time: 0.1 seconds\nProcess exited with code 2\nOutput:\nboom\n"); code != 2 || out != "boom\n" {
		t.Fatalf("code %d out %q", code, out)
	}
	if out, code := a.Output(protocol.ToolShell, `{"output":"ok\n","metadata":{"exit_code":0}}`); code != 0 || out != "ok\n" {
		t.Fatalf("code %d out %q", code, out)
	}
	if out, code := a.Output(protocol.ToolShell, "plain"); code != -1 || out != "plain" {
		t.Fatalf("code %d out %q", code, out)
	}
	call := agent.Call(a, "c", "apply_patch", json.RawMessage(`"*** Begin Patch\n*** End Patch\n"`))
	if call.GetKind() != protocol.ToolPatch || call.GetText() != "{\n  \"patch\": \"*** Begin Patch\\n*** End Patch\\n\"\n}" {
		t.Fatalf("patch call %+v", call)
	}
}
