package claude

import (
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

// A skill listing names the skills Claude Code brings along, beside the
// user's and the plugins', each a command described by its first line.
func TestASkillListingNamesCommands(t *testing.T) {
	line := []byte(`{"type":"attachment","attachment":{"type":"skill_listing","content":"- simplify: Review the changed code\n- loop: Run a prompt on an interval\nTRIGGER — more of loop\n- docs:pdf: Read PDFs","names":["simplify","loop","docs:pdf"]}}`)
	got := (&Agent{}).LogCommands(line)
	want := []agent.Command{
		{Name: "simplify", Description: "Review the changed code"},
		{Name: "loop", Description: "Run a prompt on an interval"},
		{Name: "docs:pdf", Description: "Read PDFs", Source: "docs"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if (&Agent{}).LogCommands([]byte(`{"type":"user","message":{"content":"skill_listing"}}`)) != nil {
		t.Error("a message that names a listing is none")
	}
}
