package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFile(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(b)
}

func gone(t *testing.T, root, name string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
		t.Fatalf("%s is still there (%v)", name, err)
	}
}

func turn(t *testing.T, root, session, prompt string) CheckpointEntry {
	t.Helper()
	e, err := CaptureSnapshot(context.Background(), root, Checkpoint{Kind: CheckpointTurn, Session: session, Prompt: PromptKey(prompt)})
	if err != nil {
		t.Fatalf("CaptureSnapshot: %v", err)
	}
	return e
}

func TestCaptureListAndRestoreSnapshot(t *testing.T) {
	root, runGit := cliRepo(t)
	ctx := context.Background()

	write(t, root, ".gitignore", "ignored.log\n")
	write(t, root, "a.txt", "v1\n")
	write(t, root, "staged.txt", "s1\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "init")

	// The work tree as the turn finds it: an edit, a new file, an ignored
	// one, and a change staged.
	write(t, root, "a.txt", "v2\n")
	write(t, root, "keep.txt", "before-turn\n")
	write(t, root, "ignored.log", "do-not-touch\n")
	write(t, root, "staged.txt", "s2\n")
	runGit("add", "staged.txt")
	stagedBefore := runGit("diff", "--cached")

	cp := turn(t, root, "sess1", "  fix it\r\n")
	if len(cp.Commit) != 40 || cp.Kind != CheckpointTurn || cp.Prompt != PromptKey("fix it") || cp.SessionID != "sess1" {
		t.Fatalf("checkpoint %+v", cp)
	}
	if st := strings.TrimSpace(runGit("stash", "list")); st != "" {
		t.Fatalf("CaptureSnapshot polluted the stash list: %q", st)
	}
	if got := runGit("diff", "--cached"); got != stagedBefore {
		t.Fatalf("CaptureSnapshot changed the index:\n%s", got)
	}

	// What the turn does: an edit, a new file, a file removed, a file made
	// a folder, and a change staged.
	write(t, root, "a.txt", "broken-by-agent\n")
	write(t, root, "extra/deep/new.txt", "unwanted-file\n")
	_ = os.Remove(filepath.Join(root, "keep.txt"))
	_ = os.Remove(filepath.Join(root, "staged.txt"))
	write(t, root, "staged.txt/inner.txt", "dir now\n")
	write(t, root, "other.txt", "o\n")
	runGit("add", "other.txt")
	stagedAfterTurn := runGit("diff", "--cached", "--name-status")

	list, err := ListCheckpoints(ctx, root, "sess1")
	if err != nil || len(list) != 1 || list[0] != cp {
		t.Fatalf("ListCheckpoints = %+v, %v; want %+v", list, err, cp)
	}

	safety, err := RestoreSnapshot(ctx, root, cp.Commit)
	if err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	for name, want := range map[string]string{"a.txt": "v2\n", "keep.txt": "before-turn\n", "staged.txt": "s2\n", "ignored.log": "do-not-touch\n"} {
		if got := readFile(t, root, name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	gone(t, root, "extra") // and its empty folders with it
	gone(t, root, "other.txt")
	// The index is the user's: what was staged stays so.
	if got := runGit("diff", "--cached", "--name-status"); got != stagedAfterTurn {
		t.Fatalf("restore changed the index:\n%s\nwant\n%s", got, stagedAfterTurn)
	}

	// The restore's safety snapshot is listed, and undoes it.
	if safety.Kind != CheckpointPreRestore || safety.Commit == "" {
		t.Fatalf("safety %+v", safety)
	}
	list, err = ListCheckpoints(ctx, root, "another session")
	if err != nil || len(list) != 1 || list[0] != safety {
		t.Fatalf("restores listed = %+v, %v; want %+v", list, err, safety)
	}
	if _, err := RestoreSnapshot(ctx, root, safety.Commit); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, root, "a.txt"); got != "broken-by-agent\n" {
		t.Fatalf("undone a.txt = %q", got)
	}
	if got := readFile(t, root, "staged.txt/inner.txt"); got != "dir now\n" {
		t.Fatalf("undone folder = %q", got)
	}
	gone(t, root, "keep.txt")
}

func TestRestoreSnapshotBeforeTheFirstCommit(t *testing.T) {
	root, runGit := cliRepo(t)
	ctx := context.Background()
	write(t, root, "a.txt", "one\n")
	cp := turn(t, root, "s", "start")
	write(t, root, "a.txt", "two\n")
	write(t, root, "b.txt", "new\n")
	if _, err := RestoreSnapshot(ctx, root, cp.Commit); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	if got := readFile(t, root, "a.txt"); got != "one\n" {
		t.Fatalf("a.txt = %q", got)
	}
	gone(t, root, "b.txt")
	if staged := strings.TrimSpace(runGit("ls-files", "--cached")); staged != "" {
		t.Fatalf("restore staged %q", staged)
	}
}

func TestRestoreSnapshotOfASubfolder(t *testing.T) {
	root, runGit := cliRepo(t)
	ctx := context.Background()
	write(t, root, "app/a.txt", "a1\n")
	write(t, root, "lib/l.txt", "l1\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "init")
	sub := filepath.Join(root, "app")

	cp := turn(t, sub, "s", "go")
	write(t, root, "app/a.txt", "a2\n")
	write(t, root, "app/new.txt", "n\n")
	write(t, root, "lib/l.txt", "l2\n")
	runGit("add", "lib/l.txt")
	if _, err := RestoreSnapshot(ctx, sub, cp.Commit); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	if got := readFile(t, root, "app/a.txt"); got != "a1\n" {
		t.Fatalf("app/a.txt = %q", got)
	}
	gone(t, root, "app/new.txt")
	// Outside the folder, nothing changes, staged or not.
	if got := readFile(t, root, "lib/l.txt"); got != "l2\n" {
		t.Fatalf("lib/l.txt = %q", got)
	}
	if got := strings.TrimSpace(runGit("diff", "--cached", "--name-only")); got != "lib/l.txt" {
		t.Fatalf("staged = %q", got)
	}
}

// A restore whose safety snapshot fails changes nothing.
func TestRestoreSnapshotNeedsItsSafetySnapshot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	root, runGit := cliRepo(t)
	ctx := context.Background()
	write(t, root, "a.txt", "one\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "init")
	cp := turn(t, root, "s", "x")
	write(t, root, "a.txt", "two\n")
	write(t, root, "secret.txt", "s\n")
	if err := os.Chmod(filepath.Join(root, "secret.txt"), 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(root, "secret.txt"), 0o600)

	if _, err := RestoreSnapshot(ctx, root, cp.Commit); err == nil || !strings.Contains(err.Error(), "safety snapshot") {
		t.Fatalf("restore without a safety snapshot: %v", err)
	}
	if got := readFile(t, root, "a.txt"); got != "two\n" {
		t.Fatalf("a.txt = %q: the failed restore changed it", got)
	}
	if list, _ := ListCheckpoints(ctx, root, ""); len(list) != 0 {
		t.Fatalf("restores %+v", list)
	}
}

// Nothing is changed when a file to restore would take the place of what no
// snapshot holds.
func TestRestoreSnapshotKeepsIgnoredFiles(t *testing.T) {
	root, runGit := cliRepo(t)
	ctx := context.Background()
	write(t, root, "a.txt", "one\n")
	write(t, root, "out", "a file\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "init")
	cp := turn(t, root, "s", "x")

	// The turn makes out a folder, and ignores what it puts in it.
	write(t, root, "a.txt", "two\n")
	_ = os.Remove(filepath.Join(root, "out"))
	write(t, root, "out/cache.bin", "keep me\n")
	write(t, root, ".gitignore", "out/\n")
	if _, err := RestoreSnapshot(ctx, root, cp.Commit); err == nil || !strings.Contains(err.Error(), "out/cache.bin") {
		t.Fatalf("restore over an ignored file: %v", err)
	}
	if got := readFile(t, root, "out/cache.bin"); got != "keep me\n" {
		t.Fatalf("ignored file = %q", got)
	}
	if got := readFile(t, root, "a.txt"); got != "two\n" {
		t.Fatalf("a.txt = %q: the refused restore changed it", got)
	}
}

// Git's stderr, as GIT_TRACE has it say much, is no part of a hash read.
func TestCaptureSnapshotReadsStdoutAlone(t *testing.T) {
	root, runGit := cliRepo(t)
	write(t, root, "a.txt", "one\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "init")
	t.Setenv("GIT_TRACE", "1")
	cp := turn(t, root, "s", "x")
	if err := checkHash(cp.Commit); err != nil || len(cp.Commit) != 40 {
		t.Fatalf("commit %q: %v", cp.Commit, err)
	}
}

func TestCheckpointsArePruned(t *testing.T) {
	root, runGit := cliRepo(t)
	ctx := context.Background()
	write(t, root, "a.txt", "one\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "init")
	keepTurns, keepRestores := maxTurnCheckpoints, maxRestoreCheckpoints
	defer func() { maxTurnCheckpoints, maxRestoreCheckpoints = keepTurns, keepRestores }()
	maxTurnCheckpoints, maxRestoreCheckpoints = 2, 1

	var turns []CheckpointEntry
	for i := range 4 {
		write(t, root, "a.txt", strings.Repeat("x", i+1))
		turns = append(turns, turn(t, root, "s1", "p"))
		time.Sleep(2 * time.Millisecond) // a key of its own
	}
	other := turn(t, root, "s2", "q")
	for range 2 {
		if _, err := RestoreSnapshot(ctx, root, turns[3].Commit); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	list, err := ListCheckpoints(ctx, root, "s1")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, c := range list {
		kinds = append(kinds, c.Kind)
	}
	// The session's two newest turns, the newest restore; the other
	// session's own.
	if len(list) != 3 || list[0].Kind != CheckpointPreRestore || list[1] != turns[3] || list[2] != turns[2] {
		t.Fatalf("kept %v: %+v", kinds, list)
	}
	if l, _ := ListCheckpoints(ctx, root, "s2"); len(l) != 2 || l[1] != other {
		t.Fatalf("other session kept %+v", l)
	}

	// Closing a session drops its turns, not what restores found.
	if err := DeleteCheckpoints(ctx, root, "s1"); err != nil {
		t.Fatal(err)
	}
	if l, _ := ListCheckpoints(ctx, root, "s1"); len(l) != 1 || l[0].Kind != CheckpointPreRestore {
		t.Fatalf("after close %+v", l)
	}

	// None is kept past its age but the one just made.
	if err := pruneCheckpoints(ctx, root, time.Now().Add(checkpointMaxAge+time.Hour), checkpointRefs+"s2/"+other.Key); err != nil {
		t.Fatal(err)
	}
	if l, _ := ListCheckpoints(ctx, root, "s2"); len(l) != 1 || l[0] != other {
		t.Fatalf("after aging %+v", l)
	}
}

func TestCaptureSnapshotRefusesWhatIsNoWorkTree(t *testing.T) {
	if _, err := CaptureSnapshot(context.Background(), t.TempDir(), Checkpoint{Kind: CheckpointTurn, Session: "s"}); err == nil {
		t.Fatal("captured a folder that is no repository")
	}
	root, _ := cliRepo(t)
	for _, sid := range []string{"", "../x", "a/b", "-x"} {
		if _, err := CaptureSnapshot(context.Background(), root, Checkpoint{Kind: CheckpointTurn, Session: sid}); err == nil {
			t.Fatalf("session %q accepted", sid)
		}
	}
}

func TestCaptureAndRestoreInPathWithSpaces(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "My Repo Space")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	runGit := func(args ...string) string {
		t.Helper()
		out, err := gitOut(ctx, root, nil, append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return string(out)
	}
	runGit("init", "-q")
	write(t, root, "a.txt", "before\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "init")

	cp := turn(t, root, "s1", "space path")
	write(t, root, "a.txt", "after\n")
	if _, err := RestoreSnapshot(ctx, root, cp.Commit); err != nil {
		t.Fatalf("RestoreSnapshot in path with spaces: %v", err)
	}
	if got := readFile(t, root, "a.txt"); got != "before\n" {
		t.Fatalf("a.txt = %q, want before", got)
	}
}

func TestPreRestoreCheckpointsIsolatedAcrossWorktrees(t *testing.T) {
	root, runGit := cliRepo(t)
	ctx := context.Background()
	write(t, root, "a.txt", "main-v1\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "init")

	_, wtPath, _, err := AddWorktree(ctx, root, "feat-wt", "HEAD")
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	wtCP := turn(t, wtPath, "sess-wt", "wt turn")
	write(t, wtPath, "a.txt", "wt-v2\n")
	safety, err := RestoreSnapshot(ctx, wtPath, wtCP.Commit)
	if err != nil {
		t.Fatalf("RestoreSnapshot in worktree: %v", err)
	}
	wtList, err := ListCheckpoints(ctx, wtPath, "other")
	if err != nil || len(wtList) != 1 || wtList[0].Key != safety.Key {
		t.Fatalf("worktree list = %+v, %v; want safety %+v", wtList, err, safety)
	}
	mainList, err := ListCheckpoints(ctx, root, "other")
	if err != nil {
		t.Fatalf("main ListCheckpoints: %v", err)
	}
	if len(mainList) != 0 {
		t.Fatalf("main worktree leaked linked worktree pre-restore snapshots: %+v", mainList)
	}
}

func TestRestoreRefusedLeavesNoPhantomSafetySnapshot(t *testing.T) {
	root, runGit := cliRepo(t)
	ctx := context.Background()
	write(t, root, "out", "file\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "init")
	cp := turn(t, root, "s", "x")
	_ = os.Remove(filepath.Join(root, "out"))
	write(t, root, "out/cache.bin", "keep\n")
	write(t, root, ".gitignore", "out/\n")

	if _, err := RestoreSnapshot(ctx, root, cp.Commit); err == nil {
		t.Fatal("expected restore over ignored file to fail")
	}
	list, err := ListCheckpoints(ctx, root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("refused restore left phantom pre-restore refs: %+v", list)
	}
}

func TestRestoreCaseOnlyRename(t *testing.T) {
	root, runGit := cliRepo(t)
	ctx := context.Background()
	write(t, root, "Foo.txt", "hello\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "init")
	cp := turn(t, root, "s", "case")

	// Rename Foo.txt -> foo.txt via a two-step rename so it works on both
	// case-insensitive (macOS/Windows) and case-sensitive (Linux) filesystems.
	if err := os.Rename(filepath.Join(root, "Foo.txt"), filepath.Join(root, "Foo.txt.tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "Foo.txt.tmp"), filepath.Join(root, "foo.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreSnapshot(ctx, root, cp.Commit); err != nil {
		t.Fatalf("RestoreSnapshot on case-only rename: %v", err)
	}
	if got := readFile(t, root, "Foo.txt"); got != "hello\n" {
		t.Fatalf("Foo.txt = %q", got)
	}
}
