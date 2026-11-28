package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestStageAndUnstageHunk(t *testing.T) {
	root, runGit := cliRepo(t)
	ctx := context.Background()

	var lines []string
	for i := range 20 {
		lines = append(lines, string(rune('a'+i)))
	}
	write(t, root, "f.txt", strings.Join(lines, "\n")+"\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "init")

	lines[1] = "B"
	lines[15] = "P"
	write(t, root, "f.txt", strings.Join(lines, "\n")+"\n")

	d, err := Diff(root, "f.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	var hunks [][]*protocol.DiffLine
	for _, l := range d.GetLines() {
		if l.GetOp() == "@" {
			hunks = append(hunks, nil)
			continue
		}
		hunks[len(hunks)-1] = append(hunks[len(hunks)-1], l)
	}
	if len(hunks) != 2 {
		t.Fatalf("expected 2 hunks, got %d", len(hunks))
	}

	if err := StageHunk(ctx, root, "f.txt", hunks[0], false); err != nil {
		t.Fatalf("StageHunk: %v", err)
	}
	staged := runGit("diff", "--cached")
	if !strings.Contains(staged, "+B") || strings.Contains(staged, "+P") {
		t.Fatalf("expected only first hunk staged, got:\n%s", staged)
	}

	if err := StageHunk(ctx, root, "f.txt", hunks[0], true); err != nil {
		t.Fatalf("UnstageHunk: %v", err)
	}
	if stagedAfter := strings.TrimSpace(runGit("diff", "--cached")); stagedAfter != "" {
		t.Fatalf("expected empty staged diff after unstage, got:\n%s", stagedAfter)
	}
}

func TestCaptureListAndRestoreSnapshot(t *testing.T) {
	root, runGit := cliRepo(t)
	ctx := context.Background()

	write(t, root, ".gitignore", "ignored.log\n")
	write(t, root, "a.txt", "v1\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "init")

	// Local uncommitted state before agent turn.
	write(t, root, "a.txt", "v2\n")
	write(t, root, "keep.txt", "before-turn\n")
	write(t, root, "ignored.log", "do-not-touch\n")

	sha, err := CaptureSnapshot(ctx, root, "sess-1", "turn-1")
	if err != nil {
		t.Fatalf("CaptureSnapshot: %v", err)
	}
	if len(sha) != 40 {
		t.Fatalf("expected 40-char sha, got %q", sha)
	}
	// Real stash list must remain completely empty.
	if st := strings.TrimSpace(runGit("stash", "list")); st != "" {
		t.Fatalf("CaptureSnapshot polluted stash list: %q", st)
	}

	// Simulate destructive agent edits on the turn.
	write(t, root, "a.txt", "broken-by-agent\n")
	write(t, root, "extra.txt", "unwanted-file\n")
	_ = os.Remove(filepath.Join(root, "keep.txt"))

	list, err := ListCheckpoints(ctx, root, "sess-1")
	if err != nil || len(list) != 1 || list[0].Commit != sha {
		t.Fatalf("ListCheckpoints = %+v, %v", list, err)
	}

	if err := RestoreSnapshot(ctx, root, sha); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "v2\n" {
		t.Fatalf("a.txt = %q, want v2", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "keep.txt")); string(got) != "before-turn\n" {
		t.Fatalf("keep.txt = %q, want before-turn", got)
	}
	if _, err := os.Stat(filepath.Join(root, "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("extra.txt should have been removed on restore")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "ignored.log")); string(got) != "do-not-touch\n" {
		t.Fatalf("ignored.log must be preserved, got %q", got)
	}
}
