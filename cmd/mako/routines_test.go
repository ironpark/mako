package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/routines"
)

func TestRoutinesCLILifecycle(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer

	if err := routinesAdd(dir, []string{
		"-name", "Nightly lint",
		"-project", dir,
		"-agent", "claude",
		"-schedule", "0 3 * * *",
		"-worktree",
		"-prompt", "Run golangci-lint and summarize warnings.",
	}, &buf); err != nil {
		t.Fatalf("routinesAdd: %v", err)
	}
	if !strings.Contains(buf.String(), "added routine") {
		t.Fatalf("unexpected output: %s", buf.String())
	}

	buf.Reset()
	if err := routinesList(dir, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Nightly lint") || !strings.Contains(buf.String(), "0 3 * * *") {
		t.Fatalf("routinesList = %s", buf.String())
	}

	buf.Reset()
	if err := routinesSetEnabled(dir, "Nightly lint", false, &buf); err != nil {
		t.Fatal(err)
	}
	list, err := routines.Load(dir)
	if err != nil || len(list) != 1 || list[0].Enabled {
		t.Fatalf("expected disabled routine, got %+v %v", list, err)
	}

	buf.Reset()
	if err := routinesRemove(dir, list[0].ID, &buf); err != nil {
		t.Fatal(err)
	}
	list, _ = routines.Load(dir)
	if len(list) != 0 {
		t.Fatalf("expected empty list after remove, got %+v", list)
	}
}
