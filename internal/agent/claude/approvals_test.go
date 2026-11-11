package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// A permission's suggestions are offered as Claude's dialog offers them: a
// mode, or rules that allow; others, which mako cannot say, are left out,
// and each is given back by its place.
func TestApprovalOptions(t *testing.T) {
	ev := agent.Event{Suggestions: json.RawMessage(`[
		{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"npm test:*"},{"toolName":"WebSearch"}],"behavior":"allow","destination":"localSettings"},
		{"type":"addDirectories","directories":["/tmp"],"destination":"session"},
		{"type":"setMode","mode":"acceptEdits","destination":"session"}]`)}
	got := (&Agent{}).Options(protocol.ToolShell, ev)
	if len(got) != 2 || got[0].GetId() != "0" || strings.Join(got[0].GetRules(), " ") != "Bash(npm test:*) WebSearch" ||
		got[1].GetId() != "2" || got[1].GetMode() != "acceptEdits" {
		t.Fatalf("options %+v", got)
	}
	reply := string((&Agent{}).AllowReply(protocol.ToolShell, ev, "2"))
	if !strings.Contains(reply, `"updatedPermissions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}]`) {
		t.Fatalf("reply %s", reply)
	}
	if reply := string((&Agent{}).AllowReply(protocol.ToolShell, ev, "")); strings.Contains(reply, "updatedPermissions") {
		t.Fatalf("once: %s", reply)
	}
	if got := (&Agent{}).Options(protocol.ToolExitPlan, agent.Event{}); len(got) != len(planModes) || got[0].GetMode() != "auto" {
		t.Fatalf("plan options %+v", got)
	}
	kind, canon := (&Agent{}).Tool("ExitPlanMode", json.RawMessage(`{"plan":"# Plan: X\n1. Y","planFilePath":"/p"}`))
	if kind != protocol.ToolExitPlan || agent.Detail(kind, canon) != "Plan: X" {
		t.Fatalf("plan %q %v %q", kind, canon, agent.Detail(kind, canon))
	}
}
