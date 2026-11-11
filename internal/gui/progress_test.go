package gui

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// The latest list stands for the agent's progress: those done, and the
// step in hand, or the next where none is.
func TestPlanStanding(t *testing.T) {
	messages := []*protocol.ChatMessage{
		{Role: "tool", Kind: protocol.ToolTodo, Text: `{"items":[{"text":"old","status":"pending"}]}`},
		{Role: "assistant", Text: "on it"},
		{Role: "tool", Kind: protocol.ToolPlan, Text: `{"items":[{"text":"a","status":"completed"},{"text":"b","status":"in_progress"},{"text":"c","status":"pending"}]}`},
		{Role: "tool", Kind: protocol.ToolShell, Text: `{"command":"ls"}`},
	}
	steps := planSteps(messages)
	if done, current := planStanding(steps); len(steps) != 3 || done != 1 || current != "b" {
		t.Fatalf("steps %v: done %d, current %q", steps, done, current)
	}
	none := `{"items":[{"text":"a","status":"completed"},{"text":"c","status":"pending"}]}`
	if _, current := planStanding(planSteps([]*protocol.ChatMessage{{Role: "tool", Kind: protocol.ToolTodo, Text: none}})); current != "c" {
		t.Fatalf("with none in hand, current %q", current)
	}
	if steps := planSteps(messages[1:2]); steps != nil {
		t.Fatalf("no list read as %v", steps)
	}
}
