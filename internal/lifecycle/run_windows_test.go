package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A branch named to run a command through cmd's %NAME% runs nothing: the
// hook is refused before cmd starts. One that reads it from its
// environment runs.
func TestRunRefusesAValueCmdWouldReadAsCommands(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	ev := Event{Name: TurnDone, Project: dir, Branch: "x&echo ran>" + marker}
	_, err := Run(t.Context(), Hook{Event: TurnDone, Command: "echo %AGENT_MAKO_BRANCH%"}, ev)
	if err == nil || !strings.Contains(err.Error(), "AGENT_MAKO_BRANCH") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the branch's command ran")
	}
	if _, err := Run(t.Context(), Hook{Event: TurnDone, Command: "set AGENT_MAKO_BRANCH"}, ev); err != nil {
		t.Fatalf("a hook reading its environment: %v", err)
	}
}
